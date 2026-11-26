use std::collections::BTreeSet;
use std::ffi::{CStr, CString, c_char, c_int};
use std::future::Future;
use std::pin::Pin;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex, OnceLock, mpsc};
use std::task::{Context, Poll, Waker};
use std::time::Instant;

use oxc_allocator::Allocator;
use oxc_ast::ast::{BindingPattern, Statement};
use oxc_parser::{ParseOptions, Parser};
use oxc_span::SourceType;
use psychoscript::{
    Data, DiagnosticKind, Failure, HostFunction, HostResult, HostString, HostValue, Limits,
    Namespace, ProgramOptions, Resolved, RunOutcome, Runtime, Translate, TranslateFunctions,
    TranslatePromises, from_bytes, to_bytes,
};
use serde_json::{Map, Value as J, json};

pub type KpToolCb = unsafe extern "C" fn(
    ctx_id: usize,
    tool_name: *const c_char,
    args_json: *const c_char,
    start_async: c_int,
    out_json: *mut *mut c_char,
) -> c_int;

pub type KpFreeCb = unsafe extern "C" fn(ptr: *mut c_char);

pub struct KpEngine {
    tool_cb: KpToolCb,
    free_cb: KpFreeCb,
    ctx_id: usize,
    bindings: Map<String, J>,
    snapshot_bytes: Option<Vec<u8>>,
    last_cut: Option<J>,
    build_id: String,
    active: Arc<Mutex<Option<ActiveControl>>>,
}

#[derive(Clone)]
struct ActiveControl {
    suspender: psychoscript::Suspender,
    cut_requested: Arc<AtomicBool>,
    cut_reason: Arc<Mutex<Option<String>>>,
    killed: Arc<AtomicBool>,
}

struct EvalSessionState {
    bindings: Arc<Mutex<Map<String, J>>>,
    lost: Arc<Mutex<Vec<String>>>,
    cut_started: Arc<AtomicBool>,
    cut_requested: Arc<AtomicBool>,
    killed: Arc<AtomicBool>,
}

type WorkerTask = Box<dyn FnOnce() + Send + 'static>;

fn worker_tx() -> &'static mpsc::Sender<WorkerTask> {
    static TX: OnceLock<mpsc::Sender<WorkerTask>> = OnceLock::new();
    TX.get_or_init(|| {
        let (tx, rx) = mpsc::channel::<WorkerTask>();
        let rx = Arc::new(Mutex::new(rx));
        for idx in 0..8 {
            let rx = rx.clone();
            let _ = std::thread::Builder::new()
                .name(format!("kp-psycho-worker-{idx}"))
                .spawn(move || {
                    loop {
                        let task = {
                            let guard = rx.lock().unwrap_or_else(|e| e.into_inner());
                            guard.recv()
                        };
                        match task {
                            Ok(job) => job(),
                            Err(_) => break,
                        }
                    }
                });
        }
        tx
    })
}

struct PoolFuture {
    state: Arc<Mutex<PoolFutureState>>,
    job: Option<WorkerTask>,
}

struct PoolFutureState {
    result: Option<HostResult>,
    waker: Option<Waker>,
}

impl Future for PoolFuture {
    type Output = HostResult;

    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<HostResult> {
        if let Some(job) = self.job.take() {
            if worker_tx().send(job).is_err() {
                return Poll::Ready(Err(Failure::new("InternalError", "worker pool stopped").into()));
            }
        }
        let mut s = self.state.lock().unwrap_or_else(|e| e.into_inner());
        if let Some(res) = s.result.take() {
            return Poll::Ready(res);
        }
        s.waker = Some(cx.waker().clone());
        Poll::Pending
    }
}

fn spawn_tool_future(
    tool_cb: KpToolCb,
    free_cb: KpFreeCb,
    ctx_id: usize,
    name: String,
    args_json: String,
    start_async: bool,
    eval_state: Arc<EvalSessionState>,
) -> PoolFuture {
    let state = Arc::new(Mutex::new(PoolFutureState {
        result: None,
        waker: None,
    }));
    let st_done = state.clone();
    let job: WorkerTask = Box::new(move || {
        let res = invoke_host_tool(
            tool_cb,
            free_cb,
            ctx_id,
            &name,
            &args_json,
            start_async,
            &eval_state,
        );
        let waker = {
            let mut guard = st_done.lock().unwrap_or_else(|e| e.into_inner());
            guard.result = Some(res);
            guard.waker.take()
        };
        if let Some(w) = waker {
            w.wake();
        }
    });
    PoolFuture {
        state,
        job: Some(job),
    }
}

fn invoke_host_tool(
    tool_cb: KpToolCb,
    free_cb: KpFreeCb,
    ctx_id: usize,
    name: &str,
    args_json: &str,
    start_async: bool,
    eval_state: &EvalSessionState,
) -> HostResult {
    if eval_state.killed.load(Ordering::Relaxed) {
        return Err(HostValue::Interrupt);
    }
    let c_name = match CString::new(name) {
        Ok(s) => s,
        Err(_) => return Err(Failure::new("InvalidToolName", "tool name contains NUL").into()),
    };
    let c_args = match CString::new(args_json) {
        Ok(s) => s,
        Err(_) => return Err(Failure::new("InvalidArgs", "tool args contain NUL").into()),
    };
    let mut out_ptr: *mut c_char = std::ptr::null_mut();
    let rc = unsafe {
        tool_cb(
            ctx_id,
            c_name.as_ptr(),
            c_args.as_ptr(),
            if start_async { 1 } else { 0 },
            &mut out_ptr,
        )
    };
    let payload = if !out_ptr.is_null() {
        let s = unsafe { CStr::from_ptr(out_ptr) }
            .to_string_lossy()
            .into_owned();
        unsafe { free_cb(out_ptr) };
        s
    } else {
        String::new()
    };
    let parsed: J = serde_json::from_str(&payload).unwrap_or(J::Null);
    match rc {
        0 => {
            let val = parsed.get("value").cloned().unwrap_or(J::Null);
            Ok(HostValue::from_json(&val))
        }
        1 => {
            if start_async {
                let handle = parsed.get("handle").cloned().unwrap_or(J::Null);
                Ok(HostValue::from_json(&handle))
            } else {
                eval_state.cut_started.store(true, Ordering::SeqCst);
                Err(HostValue::Interrupt)
            }
        }
        2 => {
            eval_state.cut_requested.store(true, Ordering::SeqCst);
            Err(HostValue::Interrupt)
        }
        _ => {
            let kind = parsed
                .get("kind")
                .and_then(|v| v.as_str())
                .unwrap_or("ExecutionFailure");
            let msg = parsed
                .get("message")
                .and_then(|v| v.as_str())
                .unwrap_or("host tool failed");
            Err(Failure::new(kind, msg).into())
        }
    }
}

fn contains_function_marker(v: &HostValue) -> bool {
    match v {
        HostValue::FunctionMarker | HostValue::Function(_) => true,
        HostValue::Array(items) => items
            .iter()
            .any(|it| it.as_ref().is_some_and(contains_function_marker)),
        HostValue::Object(fields) => fields.iter().any(|(_, val)| contains_function_marker(val)),
        _ => false,
    }
}

fn collect_pattern_names(pat: &BindingPattern<'_>, out: &mut Vec<String>) {
    match pat {
        BindingPattern::BindingIdentifier(id) => out.push(id.name.to_string()),
        BindingPattern::ObjectPattern(obj) => {
            for prop in &obj.properties {
                collect_pattern_names(&prop.value, out);
            }
            if let Some(rest) = &obj.rest {
                collect_pattern_names(&rest.argument, out);
            }
        }
        BindingPattern::ArrayPattern(arr) => {
            for elem in arr.elements.iter().flatten() {
                collect_pattern_names(elem, out);
            }
            if let Some(rest) = &arr.rest {
                collect_pattern_names(&rest.argument, out);
            }
        }
        BindingPattern::AssignmentPattern(assign) => {
            collect_pattern_names(&assign.left, out);
        }
    }
}

const PREAMBLE: &str = concat!(
    r#"const tools = (() => { "#,
    r#"const o = { "#,
    r#"all: (ps) => Promise.all(ps), "#,
    r#"handles: __kp_tools.handles, "#,
    r#"start: __kp_tools.start, "#,
    r#"map: async (items, fn, opts) => { "#,
    r#"const c = Math.max(1, (opts && opts.concurrency) || items.length || 1); "#,
    r#"const mid = await __kp_map_begin({ items }); "#,
    r#"const out = new Array(items.length); "#,
    r#"let next = 0; "#,
    r#"const worker = async () => { "#,
    r#"while (next < items.length) { "#,
    r#"const idx = next++; "#,
    r#"await __kp_map_start({ map_id: mid, index: idx }); "#,
    r#"const v = await fn(items[idx], idx); "#,
    r#"out[idx] = v; "#,
    r#"await __kp_map_finish({ map_id: mid, index: idx, value: v }); "#,
    r#"} "#,
    r#"}; "#,
    r#"await Promise.all(Array.from({ length: Math.min(c, items.length) }, () => worker())); "#,
    r#"return out; "#,
    r#"} "#,
    r#"}; "#,
    r#"for (const k of Object.keys(__kp_tools)) { if (k !== "handles" && k !== "start") o[k] = __kp_tools[k]; } "#,
    r#"return o; "#,
    r#"})(); "#,
    "\n"
);

/// Instruments top-level declarations (`const`, `let`, `var`, `function`) with immediate
/// `__kp_bind(name, value)` calls so state bound before any cut or return persists, and
/// non-serializable closures are caught explicitly.
fn instrument_cell(source: &str) -> String {
    let allocator = Allocator::default();
    let parsed = Parser::new(&allocator, source, SourceType::mjs())
        .with_options(ParseOptions {
            allow_return_outside_function: true,
            preserve_parens: false,
            ..ParseOptions::default()
        })
        .parse();
    if parsed.fatal_error || parsed.diagnostics.errors().next().is_some() {
        return format!("{PREAMBLE}{source}");
    }
    let mut insertions: Vec<(usize, String)> = Vec::new();
    for stmt in &parsed.program.body {
        match stmt {
            Statement::VariableDeclaration(decl) => {
                let mut names = Vec::new();
                for d in &decl.declarations {
                    collect_pattern_names(&d.id, &mut names);
                }
                if !names.is_empty() {
                    let mut hook = String::from(";");
                    for name in names {
                        hook.push_str(&format!("__kp_bind(\"{name}\",{name});"));
                    }
                    insertions.push((decl.span.end as usize, hook));
                }
            }
            Statement::FunctionDeclaration(func) => {
                if let Some(id) = &func.id {
                    let name = id.name.to_string();
                    insertions.push((
                        func.span.end as usize,
                        format!(";__kp_bind(\"{name}\",{name});"),
                    ));
                }
            }
            _ => {}
        }
    }
    if insertions.is_empty() {
        return format!("{PREAMBLE}{source}");
    }
    let mut out = String::with_capacity(PREAMBLE.len() + source.len() + insertions.len() * 24);
    out.push_str(PREAMBLE);
    let mut cursor = 0usize;
    for (pos, snippet) in insertions {
        let clamped = pos.min(source.len());
        if clamped >= cursor {
            out.push_str(&source[cursor..clamped]);
            out.push_str(&snippet);
            cursor = clamped;
        }
    }
    out.push_str(&source[cursor..]);
    out
}

fn encode_base64(input: &[u8]) -> String {
    const TABLE: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::with_capacity(input.len().div_ceil(3) * 4);
    for chunk in input.chunks(3) {
        let b0 = chunk[0] as u32;
        let b1 = if chunk.len() > 1 { chunk[1] as u32 } else { 0 };
        let b2 = if chunk.len() > 2 { chunk[2] as u32 } else { 0 };
        let triple = (b0 << 16) | (b1 << 8) | b2;
        out.push(TABLE[((triple >> 18) & 0x3f) as usize] as char);
        out.push(TABLE[((triple >> 12) & 0x3f) as usize] as char);
        out.push(if chunk.len() > 1 {
            TABLE[((triple >> 6) & 0x3f) as usize] as char
        } else {
            '='
        });
        out.push(if chunk.len() > 2 {
            TABLE[(triple & 0x3f) as usize] as char
        } else {
            '='
        });
    }
    out
}

fn decode_base64(input: &str) -> Result<Vec<u8>, String> {
    fn val(b: u8) -> Option<u32> {
        match b {
            b'A'..=b'Z' => Some((b - b'A') as u32),
            b'a'..=b'z' => Some((b - b'a' + 26) as u32),
            b'0'..=b'9' => Some((b - b'0' + 52) as u32),
            b'+' => Some(62),
            b'/' => Some(63),
            _ => None,
        }
    }
    let clean: Vec<u8> = input
        .bytes()
        .filter(|b| !b.is_ascii_whitespace())
        .collect();
    if clean.len() % 4 != 0 {
        return Err("invalid base64 length".into());
    }
    let mut out = Vec::with_capacity(clean.len() / 4 * 3);
    for chunk in clean.chunks(4) {
        let a = val(chunk[0]).ok_or_else(|| "invalid base64 char".to_string())?;
        let b = val(chunk[1]).ok_or_else(|| "invalid base64 char".to_string())?;
        let c = if chunk[2] == b'=' {
            None
        } else {
            Some(val(chunk[2]).ok_or_else(|| "invalid base64 char".to_string())?)
        };
        let d = if chunk[3] == b'=' {
            None
        } else {
            Some(val(chunk[3]).ok_or_else(|| "invalid base64 char".to_string())?)
        };
        let n = (a << 18) | (b << 12) | (c.unwrap_or(0) << 6) | d.unwrap_or(0);
        out.push(((n >> 16) & 0xff) as u8);
        if c.is_some() {
            out.push(((n >> 8) & 0xff) as u8);
        }
        if d.is_some() {
            out.push((n & 0xff) as u8);
        }
    }
    Ok(out)
}

fn build_runtime(
    engine: &KpEngine,
    tool_names: Vec<String>,
    limits: Limits,
    eval_state: Arc<EvalSessionState>,
) -> Runtime {
    let tool_cb = engine.tool_cb;
    let free_cb = engine.free_cb;
    let ctx_id = engine.ctx_id;

    let mut top_keys: BTreeSet<String> = tool_names.into_iter().collect();
    for builtin in [
        "read",
        "edit",
        "shell",
        "search",
        "subagent",
        "subagents",
        "message",
        "answer_subagent",
        "queue",
        "ask_user_async",
        "wake",
        "job",
        "sleep",
        "handles",
        "start",
    ] {
        top_keys.insert(builtin.to_string());
    }
    let top_list: Vec<HostString> = top_keys.into_iter().map(HostString::from).collect();

    Runtime::make()
        .limits(limits)
        .translate(Translate {
            functions: TranslateFunctions::Mark,
            promises: TranslatePromises::Await,
        })
        .globals(move |_run| {
            let keys_list = top_list.clone();
            let es_tools = eval_state.clone();
            let tools_ns = Namespace::new(
                move |path| match path {
                    [] => Ok(keys_list.clone()),
                    [p] if p == "handles" => {
                        Ok(vec!["get".into(), "list".into(), "cancel".into()])
                    }
                    _ => Ok(Vec::new()),
                },
                move |path| -> Resolved {
                    let parts: Vec<String> = path.iter().map(|s| s.to_string()).collect();
                    match parts.as_slice() {
                        [head, action] if head == "handles" => {
                            let full = format!("handles.{action}");
                            let es = es_tools.clone();
                            HostFunction::new(move |args, _| {
                                let args_json = encode_args_json(&args);
                                match invoke_host_tool(
                                    tool_cb, free_cb, ctx_id, &full, &args_json, false, &es,
                                ) {
                                    Ok(v) => psychoscript::Returned::Now(Ok(v)),
                                    Err(e) => psychoscript::Returned::Now(Err(e)),
                                }
                            })
                            .thenable()
                            .into()
                        }
                        [head, target] if head == "start" => {
                            let target = target.clone();
                            let es = es_tools.clone();
                            HostFunction::new(move |args, _| {
                                let args_json = encode_args_json(&args);
                                match invoke_host_tool(
                                    tool_cb, free_cb, ctx_id, &target, &args_json, true, &es,
                                ) {
                                    Ok(v) => psychoscript::Returned::Now(Ok(v)),
                                    Err(e) => psychoscript::Returned::Now(Err(e)),
                                }
                            })
                            .thenable()
                            .into()
                        }
                        [name] => {
                            let name = name.clone();
                            let es = es_tools.clone();
                            HostFunction::new_async(move |args, _| {
                                let args_json = encode_args_json(&args);
                                spawn_tool_future(
                                    tool_cb,
                                    free_cb,
                                    ctx_id,
                                    name.clone(),
                                    args_json,
                                    false,
                                    es.clone(),
                                )
                            })
                            .idempotent()
                            .into()
                        }
                        _ => Failure::missing(format!("tools.{} is not bound", parts.join(".")))
                            .into(),
                    }
                },
            );

            let es_bind = eval_state.clone();
            let bind_fn = HostFunction::new(move |args, _| {
                let name = args
                    .first()
                    .and_then(|v| v.as_str())
                    .unwrap_or("")
                    .to_string();
                let val = args.get(1).cloned().unwrap_or(HostValue::Undefined);
                if name.is_empty() {
                    return HostValue::Undefined;
                }
                if matches!(val, HostValue::Undefined) {
                    return HostValue::Undefined;
                }
                if contains_function_marker(&val) {
                    let mut lost = es_bind.lost.lock().unwrap_or_else(|e| e.into_inner());
                    if !lost.contains(&name) {
                        lost.push(name.clone());
                    }
                    es_bind
                        .bindings
                        .lock()
                        .unwrap_or_else(|e| e.into_inner())
                        .remove(&name);
                    return HostValue::Undefined;
                }
                if let Some(j) = val.to_json() {
                    es_bind
                        .bindings
                        .lock()
                        .unwrap_or_else(|e| e.into_inner())
                        .insert(name, j);
                }
                HostValue::Undefined
            });

            let map_fn = |op: &'static str, es: Arc<EvalSessionState>| {
                HostFunction::new_async(move |args, _| {
                    let args_json = encode_args_json(&args);
                    spawn_tool_future(
                        tool_cb,
                        free_cb,
                        ctx_id,
                        op.to_string(),
                        args_json,
                        false,
                        es.clone(),
                    )
                })
                .idempotent()
            };

            vec![
                ("__kp_tools", HostValue::from(tools_ns)),
                ("__kp_bind", HostValue::from(bind_fn)),
                ("__kp_map_begin", HostValue::from(map_fn("__map_begin", eval_state.clone()))),
                ("__kp_map_start", HostValue::from(map_fn("__map_start", eval_state.clone()))),
                ("__kp_map_finish", HostValue::from(map_fn("__map_finish", eval_state.clone()))),
            ]
        })
}

fn encode_args_json(args: &[HostValue]) -> String {
    if args.is_empty() {
        return "{}".into();
    }
    if args.len() == 1 {
        let j = args[0].to_json().unwrap_or(J::Null);
        return serde_json::to_string(&j).unwrap_or_else(|_| "null".into());
    }
    let arr: Vec<J> = args
        .iter()
        .map(|a| a.to_json().unwrap_or(J::Null))
        .collect();
    serde_json::to_string(&J::Array(arr)).unwrap_or_else(|_| "[]".into())
}

fn alloc_c_string(s: String) -> *mut c_char {
    match CString::new(s) {
        Ok(c) => c.into_raw(),
        Err(_) => CString::new("{\"error\":{\"kind\":\"EncodingError\",\"message\":\"interior nul\"}}")
            .unwrap()
            .into_raw(),
    }
}

#[unsafe(no_mangle)]
pub extern "C" fn kp_new(tool_cb: KpToolCb, free_cb: KpFreeCb, ctx_id: usize) -> *mut KpEngine {
    let rt = Runtime::make();
    let mut build_id = String::from("psychoscript-0.1.0");
    if let Ok(prog) = rt.program("return 1", &ProgramOptions::new()).run_resumable() {
        if let RunOutcome::Done(_) = prog {}
    }
    if let Ok(RunOutcome::Suspended(s)) = {
        let p = rt.program("await Promise.resolve(1); return 1", &ProgramOptions::new());
        let susp = p.suspender();
        let h = std::thread::spawn(move || susp.suspend(None));
        let res = p.run_resumable();
        let _ = h.join();
        res
    } {
        if let Some(b) = s.state.0.get("header").and_then(|h| h.get("build")).and_then(|v| v.as_str()) {
            build_id = b.to_string();
        }
    }
    Box::into_raw(Box::new(KpEngine {
        tool_cb,
        free_cb,
        ctx_id,
        bindings: Map::new(),
        snapshot_bytes: None,
        last_cut: None,
        build_id,
        active: Arc::new(Mutex::new(None)),
    }))
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn kp_destroy(e: *mut KpEngine) {
    if !e.is_null() {
        unsafe { drop(Box::from_raw(e)) };
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn kp_free(s: *mut c_char) {
    if !s.is_null() {
        unsafe { drop(CString::from_raw(s)) };
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn kp_eval(e: *mut KpEngine, req_json: *const c_char) -> *mut c_char {
    if e.is_null() || req_json.is_null() {
        return alloc_c_string(json!({"status":"failed","error":{"kind":"NullPointer","message":"null engine or request"}}).to_string());
    }
    let engine = unsafe { &mut *e };
    let req_str = unsafe { CStr::from_ptr(req_json) }.to_string_lossy();
    let req: J = match serde_json::from_str(&req_str) {
        Ok(v) => v,
        Err(err) => {
            return alloc_c_string(json!({"status":"failed","error":{"kind":"BadRequest","message":err.to_string()}}).to_string());
        }
    };

    let code = req.get("code").and_then(|v| v.as_str()).unwrap_or("");
    let cell_id = req.get("cell_id").and_then(|v| v.as_str()).unwrap_or("cell");
    let compute_ms = req.get("compute_ms").and_then(|v| v.as_u64()).filter(|&n| n > 0);
    let max_output_bytes = req
        .get("max_output_bytes")
        .and_then(|v| v.as_u64())
        .map(|n| n as usize);
    let max_heap_bytes = req.get("max_heap_bytes").and_then(|v| v.as_u64()).filter(|&n| n > 0);
    let resume_snapshot = req.get("resume_snapshot").and_then(|v| v.as_bool()).unwrap_or(false);

    if let Some(extra) = req.get("bindings").and_then(|v| v.as_object()) {
        for (k, v) in extra {
            engine.bindings.insert(k.clone(), v.clone());
        }
    }

    let tool_names: Vec<String> = req
        .get("tool_names")
        .and_then(|v| v.as_array())
        .map(|arr| {
            arr.iter()
                .filter_map(|v| v.as_str().map(str::to_string))
                .collect()
        })
        .unwrap_or_default();

    let limits = Limits {
        timeout_ms: compute_ms,
        max_output_bytes,
        max_heap_bytes,
        ..Limits::default()
    };

    let shared_bindings = Arc::new(Mutex::new(engine.bindings.clone()));
    let lost = Arc::new(Mutex::new(Vec::new()));
    let cut_started = Arc::new(AtomicBool::new(false));
    let cut_requested = Arc::new(AtomicBool::new(false));
    let cut_reason = Arc::new(Mutex::new(None));
    let killed = Arc::new(AtomicBool::new(false));

    let eval_state = Arc::new(EvalSessionState {
        bindings: shared_bindings.clone(),
        lost: lost.clone(),
        cut_started: cut_started.clone(),
        cut_requested: cut_requested.clone(),
        killed: killed.clone(),
    });

    let rt = build_runtime(engine, tool_names, limits, eval_state.clone());

    let mut run_bindings = shared_bindings.lock().unwrap_or_else(|e| e.into_inner()).clone();
    let kernel_obj = json!({
        "cut": engine.last_cut.clone().unwrap_or(J::Null),
        "bindings": run_bindings.keys().cloned().collect::<Vec<_>>(),
    });
    run_bindings.insert("kernel".into(), kernel_obj);
    run_bindings.insert(
        "session".into(),
        J::Object(shared_bindings.lock().unwrap_or_else(|e| e.into_inner()).clone()),
    );

    let t_compile = Instant::now();
    let full_code = instrument_cell(code);
    let compiled = if !resume_snapshot {
        match rt.compile(&full_code) {
            Ok(c) => Some(c),
            Err(d) => {
                let line = d.location.map(|(l, _)| l.saturating_sub(1)).unwrap_or(0);
                let col = d.location.map(|(_, c)| c).unwrap_or(0);
                return alloc_c_string(json!({
                    "status": "failed",
                    "error": {
                        "kind": d.kind.name(),
                        "message": d.message_lossy(),
                        "line": line,
                        "col": col,
                        "suggestions": d.suggestions_lossy(),
                    },
                    "compile_ns": t_compile.elapsed().as_nanos() as u64,
                }).to_string());
            }
        }
    } else {
        None
    };
    let compile_ns = t_compile.elapsed().as_nanos() as u64;

    let t_compute = Instant::now();
    let program_res = if resume_snapshot {
        match &engine.snapshot_bytes {
            Some(bytes) => rt.resume_bytes(bytes).map_err(|e| e.message),
            None => Err("no saved snapshot bytes to resume".into()),
        }
    } else {
        let mut opts = ProgramOptions::new().run_id(cell_id);
        let pairs: Vec<(String, Data)> = run_bindings
            .iter()
            .map(|(k, v)| (k.clone(), Data::from_json(v)))
            .collect();
        opts = opts.bindings(pairs);
        Ok(rt.program(compiled.as_ref().unwrap(), &opts))
    };

    let program = match program_res {
        Ok(p) => p,
        Err(msg) => {
            return alloc_c_string(json!({
                "status": "failed",
                "error": { "kind": "SnapshotError", "message": msg },
                "compile_ns": compile_ns,
            }).to_string());
        }
    };

    let suspender = program.suspender();
    *engine.active.lock().unwrap_or_else(|e| e.into_inner()) = Some(ActiveControl {
        suspender,
        cut_requested: cut_requested.clone(),
        cut_reason: cut_reason.clone(),
        killed: killed.clone(),
    });

    let outcome = program.run_resumable();
    *engine.active.lock().unwrap_or_else(|e| e.into_inner()) = None;
    let compute_ns = t_compute.elapsed().as_nanos() as u64;

    engine.bindings = shared_bindings.lock().unwrap_or_else(|e| e.into_inner()).clone();
    let lost_vec = lost.lock().unwrap_or_else(|e| e.into_inner()).clone();

    match outcome {
        Ok(RunOutcome::Done(out)) => {
            let logs = out.log_lines();
            match out.json() {
                Ok(val) => alloc_c_string(json!({
                    "status": "completed",
                    "value": val,
                    "logs": logs,
                    "bindings": engine.bindings,
                    "lost_bindings": lost_vec,
                    "compile_ns": compile_ns,
                    "compute_ns": compute_ns,
                }).to_string()),
                Err(d) => {
                    let status = match d.kind {
                        DiagnosticKind::TimeoutExceeded | DiagnosticKind::SliceExceeded => "killed",
                        _ => "failed",
                    };
                    let line = d.location.map(|(l, _)| l.saturating_sub(1)).unwrap_or(0);
                    let col = d.location.map(|(_, c)| c).unwrap_or(0);
                    alloc_c_string(json!({
                        "status": status,
                        "logs": logs,
                        "error": {
                            "kind": d.kind.name(),
                            "message": d.message_lossy(),
                            "line": line,
                            "col": col,
                            "suggestions": d.suggestions_lossy(),
                        },
                        "bindings": engine.bindings,
                        "lost_bindings": lost_vec,
                        "compile_ns": compile_ns,
                        "compute_ns": compute_ns,
                    }).to_string())
                }
            }
        }
        Ok(RunOutcome::Suspended(susp)) => {
            let bytes = to_bytes(&susp.state);
            let snap_len = bytes.len();
            engine.snapshot_bytes = Some(bytes.clone());
            let reason = cut_reason
                .lock()
                .unwrap_or_else(|e| e.into_inner())
                .clone()
                .unwrap_or_else(|| "waiting".into());
            alloc_c_string(json!({
                "status": "cut",
                "cut": {
                    "reason": reason,
                    "snapshot": encode_base64(&bytes),
                    "lost_bindings": lost_vec,
                },
                "bindings": engine.bindings,
                "lost_bindings": lost_vec,
                "compile_ns": compile_ns,
                "compute_ns": compute_ns,
                "snapshot_bytes": snap_len,
            }).to_string())
        }
        Err(_) => {
            if killed.load(Ordering::Relaxed) {
                return alloc_c_string(json!({
                    "status": "killed",
                    "error": { "kind": "Killed", "message": "engine killed" },
                    "bindings": engine.bindings,
                    "lost_bindings": lost_vec,
                    "compile_ns": compile_ns,
                    "compute_ns": compute_ns,
                }).to_string());
            }
            let reason = if cut_started.load(Ordering::Relaxed) {
                "started".to_string()
            } else {
                cut_reason
                    .lock()
                    .unwrap_or_else(|e| e.into_inner())
                    .clone()
                    .unwrap_or_else(|| "waiting".to_string())
            };
            alloc_c_string(json!({
                "status": "cut",
                "cut": {
                    "reason": reason,
                    "lost_bindings": lost_vec,
                },
                "bindings": engine.bindings,
                "lost_bindings": lost_vec,
                "compile_ns": compile_ns,
                "compute_ns": compute_ns,
            }).to_string())
        }
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn kp_restore(e: *mut KpEngine, snap_json: *const c_char) -> *mut c_char {
    if e.is_null() || snap_json.is_null() {
        return alloc_c_string(json!({"ok":false,"error":"null pointer"}).to_string());
    }
    let engine = unsafe { &mut *e };
    let snap_str = unsafe { CStr::from_ptr(snap_json) }.to_string_lossy();
    let v: J = match serde_json::from_str(&snap_str) {
        Ok(v) => v,
        Err(err) => return alloc_c_string(json!({"ok":false,"error":err.to_string()}).to_string()),
    };
    engine.bindings = v
        .get("bindings")
        .and_then(|b| b.as_object())
        .cloned()
        .unwrap_or_default();
    engine.last_cut = v.get("cut").cloned().filter(|c| !c.is_null());
    if let Some(b64) = v.get("engine_bytes_b64").and_then(|s| s.as_str()).filter(|s| !s.is_empty()) {
        match decode_base64(b64) {
            Ok(bytes) => {
                if let Err(err) = from_bytes(&bytes) {
                    return alloc_c_string(json!({"ok":false,"error":err.message}).to_string());
                }
                engine.snapshot_bytes = Some(bytes);
            }
            Err(err) => return alloc_c_string(json!({"ok":false,"error":err}).to_string()),
        }
    } else {
        engine.snapshot_bytes = None;
    }
    alloc_c_string(json!({"ok":true}).to_string())
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn kp_snapshot(e: *mut KpEngine) -> *mut c_char {
    if e.is_null() {
        return alloc_c_string(json!({"error":"null engine"}).to_string());
    }
    let engine = unsafe { &*e };
    let b64 = engine
        .snapshot_bytes
        .as_ref()
        .map(|b| encode_base64(b))
        .unwrap_or_default();
    alloc_c_string(json!({
        "format": 2,
        "engine": "psychoscript",
        "build_id": engine.build_id,
        "bindings": engine.bindings,
        "engine_bytes_b64": b64,
        "cut": engine.last_cut,
    }).to_string())
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn kp_cut(e: *mut KpEngine, reason: *const c_char) -> *mut c_char {
    if e.is_null() {
        return alloc_c_string(json!({"ok":false,"error":"null engine"}).to_string());
    }
    let engine = unsafe { &*e };
    let r = if reason.is_null() {
        "waiting".to_string()
    } else {
        unsafe { CStr::from_ptr(reason) }.to_string_lossy().into_owned()
    };
    let active = engine.active.lock().unwrap_or_else(|e| e.into_inner()).clone();
    let Some(ctrl) = active else {
        return alloc_c_string(json!({"ok":true,"reason":r,"idle":true}).to_string());
    };
    ctrl.cut_requested.store(true, Ordering::SeqCst);
    *ctrl.cut_reason.lock().unwrap_or_else(|e| e.into_inner()) = Some(r.clone());
    match ctrl.suspender.suspend(None) {
        Ok(RunOutcome::Suspended(s)) => {
            let bytes = to_bytes(&s.state);
            alloc_c_string(json!({"ok":true,"reason":r,"snapshot":encode_base64(&bytes)}).to_string())
        }
        Ok(RunOutcome::Done(_)) => alloc_c_string(json!({"ok":true,"reason":r,"done":true}).to_string()),
        Err(err) => alloc_c_string(json!({"ok":false,"error":err.message}).to_string()),
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn kp_kill(e: *mut KpEngine) {
    if e.is_null() {
        return;
    }
    let engine = unsafe { &*e };
    if let Some(ctrl) = engine.active.lock().unwrap_or_else(|e| e.into_inner()).clone() {
        ctrl.killed.store(true, Ordering::SeqCst);
        let _ = ctrl.suspender.suspend(Some(0));
    }
}
