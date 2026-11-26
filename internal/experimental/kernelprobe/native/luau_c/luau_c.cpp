#include "kp_abi.h"
#include "lua.h"
#include "lualib.h"
#include "luacode.h"

#include <atomic>
#include <chrono>
#include <cctype>
#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <map>
#include <set>
#include <string>
#include <vector>

namespace {

struct JsonVal {
    enum Kind { JNull, JBool, JNum, JStr, JArr, JObj };
    Kind kind = JNull;
    bool b = false;
    double n = 0.0;
    std::string s;
    std::vector<JsonVal> arr;
    std::map<std::string, JsonVal> obj;

    static JsonVal make_null() { return JsonVal{}; }
    static JsonVal make_bool(bool v) { JsonVal x; x.kind = JBool; x.b = v; return x; }
    static JsonVal make_num(double v) { JsonVal x; x.kind = JNum; x.n = v; return x; }
    static JsonVal make_str(std::string v) { JsonVal x; x.kind = JStr; x.s = std::move(v); return x; }
    static JsonVal make_arr() { JsonVal x; x.kind = JArr; return x; }
    static JsonVal make_obj() { JsonVal x; x.kind = JObj; return x; }

    const JsonVal* get(const std::string& k) const {
        if (kind != JObj) return nullptr;
        auto it = obj.find(k);
        return it == obj.end() ? nullptr : &it->second;
    }
    std::string as_str(const std::string& def = "") const {
        return kind == JStr ? s : def;
    }
    double as_num(double def = 0.0) const {
        return kind == JNum ? n : def;
    }
    bool as_bool(bool def = false) const {
        return kind == JBool ? b : def;
    }
};

void escape_json_str(const std::string& in, std::string& out) {
    out.push_back('"');
    for (unsigned char c : in) {
        switch (c) {
            case '"': out += "\\\""; break;
            case '\\': out += "\\\\"; break;
            case '\b': out += "\\b"; break;
            case '\f': out += "\\f"; break;
            case '\n': out += "\\n"; break;
            case '\r': out += "\\r"; break;
            case '\t': out += "\\t"; break;
            default:
                if (c < 0x20) {
                    char buf[8];
                    std::snprintf(buf, sizeof(buf), "\\u%04x", c);
                    out += buf;
                } else {
                    out.push_back(static_cast<char>(c));
                }
        }
    }
    out.push_back('"');
}

void dump_json_into(const JsonVal& v, std::string& out) {
    switch (v.kind) {
        case JsonVal::JNull:
            out += "null";
            break;
        case JsonVal::JBool:
            out += (v.b ? "true" : "false");
            break;
        case JsonVal::JNum: {
            if (v.n != v.n || v.n == 1.0/0.0 || v.n == -1.0/0.0) {
                out += "null";
            } else {
                char buf[64];
                double intpart;
                if (std::modf(v.n, &intpart) == 0.0 && v.n >= -9e15 && v.n <= 9e15) {
                    std::snprintf(buf, sizeof(buf), "%.0f", v.n);
                } else {
                    std::snprintf(buf, sizeof(buf), "%.17g", v.n);
                }
                out += buf;
            }
            break;
        }
        case JsonVal::JStr:
            escape_json_str(v.s, out);
            break;
        case JsonVal::JArr:
            out.push_back('[');
            for (size_t i = 0; i < v.arr.size(); ++i) {
                if (i > 0) out.push_back(',');
                dump_json_into(v.arr[i], out);
            }
            out.push_back(']');
            break;
        case JsonVal::JObj:
            out.push_back('{');
            bool first = true;
            for (const auto& kv : v.obj) {
                if (!first) out.push_back(',');
                first = false;
                escape_json_str(kv.first, out);
                out.push_back(':');
                dump_json_into(kv.second, out);
            }
            out.push_back('}');
            break;
    }
}

std::string dump_json(const JsonVal& v) {
    std::string out;
    dump_json_into(v, out);
    return out;
}

class JsonParser {
public:
    explicit JsonParser(std::string s) : src_(std::move(s)), pos_(0) {}
    bool parse(JsonVal& out) {
        skip_ws();
        if (!parse_value(out)) return false;
        skip_ws();
        return pos_ == src_.size();
    }
private:
    std::string src_;
    size_t pos_;

    void skip_ws() {
        while (pos_ < src_.size() && std::isspace(static_cast<unsigned char>(src_[pos_]))) {
            ++pos_;
        }
    }
    bool parse_value(JsonVal& out) {
        skip_ws();
        if (pos_ >= src_.size()) return false;
        char c = src_[pos_];
        if (c == 'n') return parse_lit("null", JsonVal::make_null(), out);
        if (c == 't') return parse_lit("true", JsonVal::make_bool(true), out);
        if (c == 'f') return parse_lit("false", JsonVal::make_bool(false), out);
        if (c == '"') {
            std::string s;
            if (!parse_string(s)) return false;
            out = JsonVal::make_str(std::move(s));
            return true;
        }
        if (c == '[') return parse_array(out);
        if (c == '{') return parse_object(out);
        if (c == '-' || std::isdigit(static_cast<unsigned char>(c))) return parse_number(out);
        return false;
    }
    bool parse_lit(const char* lit, JsonVal val, JsonVal& out) {
        size_t len = std::strlen(lit);
        if (src_.compare(pos_, len, lit) == 0) {
            pos_ += len;
            out = std::move(val);
            return true;
        }
        return false;
    }
    bool parse_string(std::string& out) {
        if (pos_ >= src_.size() || src_[pos_] != '"') return false;
        ++pos_;
        out.clear();
        while (pos_ < src_.size()) {
            char c = src_[pos_++];
            if (c == '"') return true;
            if (c == '\\') {
                if (pos_ >= src_.size()) return false;
                char esc = src_[pos_++];
                switch (esc) {
                    case '"': out.push_back('"'); break;
                    case '\\': out.push_back('\\'); break;
                    case '/': out.push_back('/'); break;
                    case 'b': out.push_back('\b'); break;
                    case 'f': out.push_back('\f'); break;
                    case 'n': out.push_back('\n'); break;
                    case 'r': out.push_back('\r'); break;
                    case 't': out.push_back('\t'); break;
                    case 'u': {
                        if (pos_ + 4 > src_.size()) return false;
                        unsigned int cp = 0;
                        for (int i = 0; i < 4; ++i) {
                            char h = src_[pos_++];
                            cp <<= 4;
                            if (h >= '0' && h <= '9') cp |= (h - '0');
                            else if (h >= 'a' && h <= 'f') cp |= (h - 'a' + 10);
                            else if (h >= 'A' && h <= 'F') cp |= (h - 'A' + 10);
                            else return false;
                        }
                        if (cp < 0x80) {
                            out.push_back(static_cast<char>(cp));
                        } else if (cp < 0x800) {
                            out.push_back(static_cast<char>(0xC0 | (cp >> 6)));
                            out.push_back(static_cast<char>(0x80 | (cp & 0x3F)));
                        } else {
                            out.push_back(static_cast<char>(0xE0 | (cp >> 12)));
                            out.push_back(static_cast<char>(0x80 | ((cp >> 6) & 0x3F)));
                            out.push_back(static_cast<char>(0x80 | (cp & 0x3F)));
                        }
                        break;
                    }
                    default: return false;
                }
            } else {
                out.push_back(c);
            }
        }
        return false;
    }
    bool parse_number(JsonVal& out) {
        size_t start = pos_;
        if (src_[pos_] == '-') ++pos_;
        while (pos_ < src_.size() && std::isdigit(static_cast<unsigned char>(src_[pos_]))) ++pos_;
        if (pos_ < src_.size() && src_[pos_] == '.') {
            ++pos_;
            while (pos_ < src_.size() && std::isdigit(static_cast<unsigned char>(src_[pos_]))) ++pos_;
        }
        if (pos_ < src_.size() && (src_[pos_] == 'e' || src_[pos_] == 'E')) {
            ++pos_;
            if (pos_ < src_.size() && (src_[pos_] == '+' || src_[pos_] == '-')) ++pos_;
            while (pos_ < src_.size() && std::isdigit(static_cast<unsigned char>(src_[pos_]))) ++pos_;
        }
        double v = std::strtod(src_.substr(start, pos_ - start).c_str(), nullptr);
        out = JsonVal::make_num(v);
        return true;
    }
    bool parse_array(JsonVal& out) {
        ++pos_;
        out = JsonVal::make_arr();
        skip_ws();
        if (pos_ < src_.size() && src_[pos_] == ']') { ++pos_; return true; }
        while (pos_ < src_.size()) {
            JsonVal elem;
            if (!parse_value(elem)) return false;
            out.arr.push_back(std::move(elem));
            skip_ws();
            if (pos_ < src_.size() && src_[pos_] == ']') { ++pos_; return true; }
            if (pos_ >= src_.size() || src_[pos_] != ',') return false;
            ++pos_;
        }
        return false;
    }
    bool parse_object(JsonVal& out) {
        ++pos_;
        out = JsonVal::make_obj();
        skip_ws();
        if (pos_ < src_.size() && src_[pos_] == '}') { ++pos_; return true; }
        while (pos_ < src_.size()) {
            skip_ws();
            std::string key;
            if (!parse_string(key)) return false;
            skip_ws();
            if (pos_ >= src_.size() || src_[pos_] != ':') return false;
            ++pos_;
            JsonVal val;
            if (!parse_value(val)) return false;
            out.obj[std::move(key)] = std::move(val);
            skip_ws();
            if (pos_ < src_.size() && src_[pos_] == '}') { ++pos_; return true; }
            if (pos_ >= src_.size() || src_[pos_] != ',') return false;
            ++pos_;
        }
        return false;
    }
};

char* alloc_c_str(const std::string& s) {
    char* p = static_cast<char*>(std::malloc(s.size() + 1));
    if (p) {
        std::memcpy(p, s.data(), s.size());
        p[s.size()] = '\0';
    }
    return p;
}

void push_json(lua_State* L, const JsonVal& v) {
    switch (v.kind) {
        case JsonVal::JNull:
            lua_pushnil(L);
            break;
        case JsonVal::JBool:
            lua_pushboolean(L, v.b ? 1 : 0);
            break;
        case JsonVal::JNum:
            lua_pushnumber(L, v.n);
            break;
        case JsonVal::JStr:
            lua_pushlstring(L, v.s.data(), v.s.size());
            break;
        case JsonVal::JArr:
            lua_createtable(L, static_cast<int>(v.arr.size()), 0);
            for (size_t i = 0; i < v.arr.size(); ++i) {
                push_json(L, v.arr[i]);
                lua_rawseti(L, -2, static_cast<int>(i + 1));
            }
            break;
        case JsonVal::JObj:
            lua_createtable(L, 0, static_cast<int>(v.obj.size()));
            for (const auto& kv : v.obj) {
                lua_pushlstring(L, kv.first.data(), kv.first.size());
                push_json(L, kv.second);
                lua_rawset(L, -3);
            }
            break;
    }
}

bool lua_to_json(lua_State* L, int idx, JsonVal& out, std::set<const void*>& seen, int depth) {
    if (depth > 32) return false;
    idx = lua_absindex(L, idx);
    int t = lua_type(L, idx);
    switch (t) {
        case LUA_TNIL:
            out = JsonVal::make_null();
            return true;
        case LUA_TBOOLEAN:
            out = JsonVal::make_bool(lua_toboolean(L, idx) != 0);
            return true;
        case LUA_TNUMBER:
            out = JsonVal::make_num(lua_tonumber(L, idx));
            return true;
        case LUA_TSTRING: {
            size_t len = 0;
            const char* s = lua_tolstring(L, idx, &len);
            out = JsonVal::make_str(std::string(s, len));
            return true;
        }
        case LUA_TTABLE: {
            const void* ptr = lua_topointer(L, idx);
            if (seen.count(ptr)) return false;
            seen.insert(ptr);

            int max_idx = 0;
            int count = 0;
            bool is_seq = true;
            lua_pushnil(L);
            while (lua_next(L, idx) != 0) {
                if (lua_type(L, -2) == LUA_TNUMBER) {
                    double k = lua_tonumber(L, -2);
                    double intp = 0;
                    if (std::modf(k, &intp) == 0.0 && k >= 1 && k <= 1e7) {
                        int ik = static_cast<int>(k);
                        if (ik > max_idx) max_idx = ik;
                    } else {
                        is_seq = false;
                    }
                } else {
                    is_seq = false;
                }
                ++count;
                lua_pop(L, 1);
            }

            if (count == 0) {
                // Empty table defaults to empty object unless __array hint, both round-trip cleanly.
                out = JsonVal::make_arr();
                seen.erase(ptr);
                return true;
            }
            if (is_seq && max_idx == count) {
                out = JsonVal::make_arr();
                out.arr.resize(count);
                for (int i = 1; i <= count; ++i) {
                    lua_rawgeti(L, idx, i);
                    JsonVal elem;
                    if (!lua_to_json(L, -1, elem, seen, depth + 1)) {
                        lua_pop(L, 1);
                        seen.erase(ptr);
                        return false;
                    }
                    lua_pop(L, 1);
                    out.arr[i - 1] = std::move(elem);
                }
                seen.erase(ptr);
                return true;
            }
            out = JsonVal::make_obj();
            lua_pushnil(L);
            while (lua_next(L, idx) != 0) {
                std::string k;
                if (lua_type(L, -2) == LUA_TSTRING) {
                    size_t len = 0;
                    const char* ks = lua_tolstring(L, -2, &len);
                    k.assign(ks, len);
                } else if (lua_type(L, -2) == LUA_TNUMBER) {
                    char buf[64];
                    std::snprintf(buf, sizeof(buf), "%.17g", lua_tonumber(L, -2));
                    k = buf;
                } else {
                    lua_pop(L, 2);
                    seen.erase(ptr);
                    return false;
                }
                JsonVal val;
                if (!lua_to_json(L, -1, val, seen, depth + 1)) {
                    lua_pop(L, 2);
                    seen.erase(ptr);
                    return false;
                }
                out.obj[std::move(k)] = std::move(val);
                lua_pop(L, 1);
            }
            seen.erase(ptr);
            return true;
        }
        default:
            // LUA_TFUNCTION, LUA_TUSERDATA, LUA_TTHREAD, etc. cannot serialize.
            return false;
    }
}

struct ActiveEval {
    std::atomic<bool> cut_requested{false};
    std::atomic<bool> cut_started{false};
    std::atomic<bool> killed{false};
    std::atomic<bool> timed_out{false};
    std::string cut_reason = "waiting";
    std::chrono::steady_clock::time_point deadline;
    bool has_deadline = false;
    std::vector<std::string> logs;
    size_t log_bytes = 0;
    size_t max_output_bytes = 65536;
};

} // namespace

struct kp_engine {
    kp_tool_cb tool_cb = nullptr;
    kp_free_cb free_cb = nullptr;
    uintptr_t ctx_id = 0;
    JsonVal bindings = JsonVal::make_obj();
    JsonVal last_cut = JsonVal::make_null();
    std::vector<std::string> last_lost;
    std::atomic<ActiveEval*> active{nullptr};
};

namespace {

void interrupt_cb(lua_State* L, int gc) {
    if (gc >= 0) return; // Never yield or raise during GC step.
    auto* engine = static_cast<kp_engine*>(lua_callbacks(L)->userdata);
    if (!engine) return;
    ActiveEval* ev = engine->active.load(std::memory_order_acquire);
    if (!ev) return;

    if (ev->killed.load(std::memory_order_relaxed)) {
        luaL_error(L, "Killed: engine killed");
    }
    if (ev->has_deadline && std::chrono::steady_clock::now() >= ev->deadline) {
        ev->timed_out.store(true, std::memory_order_release);
        luaL_error(L, "TimeoutExceeded: compute budget exceeded");
    }
    if (ev->cut_requested.load(std::memory_order_relaxed)) {
        if (lua_isyieldable(L)) {
            lua_break(L);
        } else {
            luaL_error(L, "__KP_CUT_REQUESTED__");
        }
    }
}

int l_kp_print(lua_State* L) {
    auto* engine = static_cast<kp_engine*>(lua_callbacks(L)->userdata);
    ActiveEval* ev = engine ? engine->active.load(std::memory_order_acquire) : nullptr;
    int n = lua_gettop(L);
    std::string line;
    for (int i = 1; i <= n; ++i) {
        if (i > 1) line.push_back('\t');
        size_t len = 0;
        const char* s = luaL_tolstring(L, i, &len);
        if (s) line.append(s, len);
        lua_pop(L, 1);
    }
    if (ev && ev->log_bytes + line.size() <= ev->max_output_bytes) {
        ev->log_bytes += line.size();
        ev->logs.push_back(std::move(line));
    }
    return 0;
}

int dispatch_tool(lua_State* L, kp_engine* engine, const char* tool_name, int start_async) {
    ActiveEval* ev = engine->active.load(std::memory_order_acquire);
    int nargs = lua_gettop(L);
    std::string args_json = "{}";
    std::set<const void*> seen;
    if (nargs == 1) {
        JsonVal arg;
        if (!lua_to_json(L, 1, arg, seen, 0)) {
            luaL_error(L, "tool argument is not JSON-serializable");
        }
        args_json = dump_json(arg);
    } else if (nargs > 1) {
        JsonVal arr = JsonVal::make_arr();
        for (int i = 1; i <= nargs; ++i) {
            JsonVal item;
            if (!lua_to_json(L, i, item, seen, 0)) {
                luaL_error(L, "tool argument is not JSON-serializable");
            }
            arr.arr.push_back(std::move(item));
        }
        args_json = dump_json(arr);
    }

    char* out_ptr = nullptr;
    int rc = engine->tool_cb(engine->ctx_id, tool_name, args_json.c_str(), start_async, &out_ptr);
    std::string payload;
    if (out_ptr) {
        payload = out_ptr;
        engine->free_cb(out_ptr);
    }
    JsonVal parsed = JsonVal::make_null();
    if (!payload.empty()) {
        JsonParser p(payload);
        p.parse(parsed);
    }

    if (rc == 0) {
        const JsonVal* val = parsed.get("value");
        if (val) push_json(L, *val);
        else lua_pushnil(L);
        return 1;
    }
    if (rc == 1) {
        if (start_async) {
            const JsonVal* h = parsed.get("handle");
            if (h) push_json(L, *h);
            else lua_pushnil(L);
            return 1;
        }
        if (ev) {
            ev->cut_started.store(true, std::memory_order_release);
            ev->cut_reason = "started";
        }
        if (lua_isyieldable(L)) {
            return lua_yield(L, 0);
        }
        luaL_error(L, "__KP_CUT_STARTED__");
        return 0;
    }
    if (rc == 2) {
        if (ev) {
            ev->cut_requested.store(true, std::memory_order_release);
        }
        if (lua_isyieldable(L)) {
            return lua_yield(L, 0);
        }
        luaL_error(L, "__KP_CUT_REQUESTED__");
        return 0;
    }
    std::string kind = "ExecutionFailure";
    std::string msg = "host tool failed";
    if (const JsonVal* k = parsed.get("kind")) kind = k->as_str(kind);
    if (const JsonVal* m = parsed.get("message")) msg = m->as_str(msg);
    luaL_error(L, "%s: %s", kind.c_str(), msg.c_str());
    return 0;
}

int l_tool_closure(lua_State* L) {
    auto* engine = static_cast<kp_engine*>(lua_callbacks(L)->userdata);
    const char* name = lua_tostring(L, lua_upvalueindex(1));
    int start_async = lua_tointeger(L, lua_upvalueindex(2));
    return dispatch_tool(L, engine, name ? name : "", start_async);
}

int l_tools_index(lua_State* L) {
    const char* key = luaL_checkstring(L, 2);
    int start_async = lua_tointeger(L, lua_upvalueindex(1));
    lua_pushstring(L, key);
    lua_pushinteger(L, start_async);
    lua_pushcclosure(L, l_tool_closure, key, 2);
    return 1;
}

int l_handles_closure(lua_State* L) {
    auto* engine = static_cast<kp_engine*>(lua_callbacks(L)->userdata);
    const char* action = lua_tostring(L, lua_upvalueindex(1));
    std::string full = std::string("handles.") + (action ? action : "");
    return dispatch_tool(L, engine, full.c_str(), 0);
}

int l_map_hook(lua_State* L) {
    auto* engine = static_cast<kp_engine*>(lua_callbacks(L)->userdata);
    const char* op = lua_tostring(L, lua_upvalueindex(1));
    return dispatch_tool(L, engine, op ? op : "__map_begin", 0);
}

const char* LUA_PRELUDE = R"lua(
tools.all = function(fns)
    local out = {}
    for i, f in ipairs(fns) do
        if type(f) == "function" then
            out[i] = f()
        else
            out[i] = f
        end
    end
    return out
end

tools.map = function(items, fn, opts)
    local mid = __kp_map_begin({ items = items })
    local out = {}
    for i, item in ipairs(items) do
        __kp_map_start({ map_id = mid, index = i - 1 })
        local v = fn(item, i - 1)
        out[i] = v
        __kp_map_finish({ map_id = mid, index = i - 1, value = v })
    end
    return out
end
)lua";

void setup_env(lua_State* L, kp_engine* engine, std::set<std::string>& baseline_keys) {
    lua_callbacks(L)->userdata = engine;
    lua_callbacks(L)->interrupt = interrupt_cb;

    luaopen_base(L);
    lua_pop(L, 1);
    luaopen_table(L);
    lua_pop(L, 1);
    luaopen_string(L);
    lua_pop(L, 1);
    luaopen_math(L);
    lua_pop(L, 1);
    luaopen_bit32(L);
    lua_pop(L, 1);
    luaopen_utf8(L);
    lua_pop(L, 1);
    luaopen_coroutine(L);
    lua_pop(L, 1);
    luaopen_buffer(L);
    lua_pop(L, 1);

    lua_pushcfunction(L, l_kp_print, "print");
    lua_setglobal(L, "print");

    // __kp_map_* helpers
    for (const char* name : {"__kp_map_begin", "__kp_map_start", "__kp_map_finish"}) {
        const char* op = name + 5; // "__map_..."
        // Map "__kp_map_begin" -> "__map_begin"
        std::string op_name = std::string("__map_") + (op + 4);
        lua_pushstring(L, op_name.c_str());
        lua_pushcclosure(L, l_map_hook, name, 1);
        lua_setglobal(L, name);
    }

    // tools table
    lua_newtable(L);

    // tools.handles
    lua_newtable(L);
    for (const char* act : {"get", "list", "cancel"}) {
        lua_pushstring(L, act);
        lua_pushcclosure(L, l_handles_closure, act, 1);
        lua_setfield(L, -2, act);
    }
    lua_setfield(L, -2, "handles");

    // tools.start
    lua_newtable(L);
    lua_newtable(L);
    lua_pushinteger(L, 1);
    lua_pushcclosure(L, l_tools_index, "start.__index", 1);
    lua_setfield(L, -2, "__index");
    lua_setmetatable(L, -2);
    lua_setfield(L, -2, "start");

    // tools metatable
    lua_newtable(L);
    lua_pushinteger(L, 0);
    lua_pushcclosure(L, l_tools_index, "tools.__index", 1);
    lua_setfield(L, -2, "__index");
    lua_setmetatable(L, -2);

    lua_setglobal(L, "tools");

    // Load LUA_PRELUDE before sandboxing so tools.all & tools.map attach cleanly.
    size_t bc_size = 0;
    lua_CompileOptions copts{};
    copts.optimizationLevel = 1;
    copts.debugLevel = 1;
    char* bc = luau_compile(LUA_PRELUDE, std::strlen(LUA_PRELUDE), &copts, &bc_size);
    if (bc) {
        if (luau_load(L, "=prelude", bc, bc_size, 0) == 0) {
            lua_pcall(L, 0, 0, 0);
        } else {
            lua_pop(L, 1);
        }
        std::free(bc);
    }

    luaL_sandbox(L);

    // Record baseline global keys after sandbox setup so we only persist user globals.
    lua_pushnil(L);
    while (lua_next(L, LUA_GLOBALSINDEX) != 0) {
        if (lua_type(L, -2) == LUA_TSTRING) {
            size_t len = 0;
            const char* k = lua_tolstring(L, -2, &len);
            baseline_keys.insert(std::string(k, len));
        }
        lua_pop(L, 1);
    }
    baseline_keys.insert("kernel");
    baseline_keys.insert("session");
}

} // namespace

extern "C" {

kp_engine* kp_new(kp_tool_cb cb, kp_free_cb free_cb, uintptr_t ctx_id) {
    auto* e = new kp_engine();
    e->tool_cb = cb;
    e->free_cb = free_cb;
    e->ctx_id = ctx_id;
    return e;
}

void kp_destroy(kp_engine* e) {
    delete e;
}

void kp_free(char* s) {
    std::free(s);
}

char* kp_eval(kp_engine* e, const char* req_json) {
    if (!e || !req_json) {
        return alloc_c_str(R"({"status":"failed","error":{"kind":"NullPointer","message":"null engine or request"}})");
    }
    JsonVal req;
    JsonParser parser(req_json);
    if (!parser.parse(req)) {
        return alloc_c_str(R"({"status":"failed","error":{"kind":"BadRequest","message":"invalid request json"}})");
    }

    std::string code = req.get("code") ? req.get("code")->as_str() : "";
    uint64_t compute_ms = req.get("compute_ms") ? static_cast<uint64_t>(req.get("compute_ms")->as_num(0)) : 0;
    size_t max_out = req.get("max_output_bytes") ? static_cast<size_t>(req.get("max_output_bytes")->as_num(65536)) : 65536;
    if (const JsonVal* extra = req.get("bindings")) {
        if (extra->kind == JsonVal::JObj) {
            for (const auto& kv : extra->obj) {
                e->bindings.obj[kv.first] = kv.second;
            }
        }
    }

    ActiveEval ev;
    ev.max_output_bytes = max_out > 0 ? max_out : 65536;
    if (compute_ms > 0) {
        ev.has_deadline = true;
        ev.deadline = std::chrono::steady_clock::now() + std::chrono::milliseconds(compute_ms);
    }

    lua_State* L = luaL_newstate();
    std::set<std::string> baseline_keys;
    setup_env(L, e, baseline_keys);

    // Create isolated sandbox thread per cell.
    lua_State* T = lua_newthread(L);
    luaL_sandboxthread(T);

    // Populate persisted user globals + kernel + session on T's globals.
    for (const auto& kv : e->bindings.obj) {
        lua_pushlstring(T, kv.first.data(), kv.first.size());
        push_json(T, kv.second);
        lua_settable(T, LUA_GLOBALSINDEX);
    }

    JsonVal kernel_obj = JsonVal::make_obj();
    kernel_obj.obj["cut"] = e->last_cut;
    JsonVal bind_names = JsonVal::make_arr();
    for (const auto& kv : e->bindings.obj) {
        bind_names.arr.push_back(JsonVal::make_str(kv.first));
    }
    kernel_obj.obj["bindings"] = std::move(bind_names);
    push_json(T, kernel_obj);
    lua_setglobal(T, "kernel");

    push_json(T, e->bindings);
    lua_setglobal(T, "session");

    auto t_compile = std::chrono::steady_clock::now();
    size_t bc_size = 0;
    lua_CompileOptions copts{};
    copts.optimizationLevel = 1;
    copts.debugLevel = 1;
    char* bc = luau_compile(code.data(), code.size(), &copts, &bc_size);
    uint64_t compile_ns = std::chrono::duration_cast<std::chrono::nanoseconds>(std::chrono::steady_clock::now() - t_compile).count();

    if (!bc) {
        lua_close(L);
        return alloc_c_str(R"({"status":"failed","error":{"kind":"CompileError","message":"luau_compile returned null"}})");
    }

    int load_rc = luau_load(T, "=cell", bc, bc_size, 0);
    std::free(bc);
    if (load_rc != 0) {
        std::string err = lua_tostring(T, -1) ? lua_tostring(T, -1) : "syntax error";
        lua_close(L);
        JsonVal resp = JsonVal::make_obj();
        resp.obj["status"] = JsonVal::make_str("failed");
        JsonVal eo = JsonVal::make_obj();
        eo.obj["kind"] = JsonVal::make_str("ParseError");
        eo.obj["message"] = JsonVal::make_str(err);
        resp.obj["error"] = std::move(eo);
        resp.obj["compile_ns"] = JsonVal::make_num(static_cast<double>(compile_ns));
        return alloc_c_str(dump_json(resp));
    }

    e->active.store(&ev, std::memory_order_release);
    auto t_compute = std::chrono::steady_clock::now();
    int resume_rc = lua_resume(T, nullptr, 0);
    uint64_t compute_ns = std::chrono::duration_cast<std::chrono::nanoseconds>(std::chrono::steady_clock::now() - t_compute).count();
    e->active.store(nullptr, std::memory_order_release);

    // Harvest serializable globals and record lost non-serializable globals (closures/userdata/cycles).
    std::vector<std::string> lost;
    JsonVal next_bindings = JsonVal::make_obj();
    lua_pushnil(T);
    while (lua_next(T, LUA_GLOBALSINDEX) != 0) {
        if (lua_type(T, -2) == LUA_TSTRING) {
            size_t len = 0;
            const char* ks = lua_tolstring(T, -2, &len);
            std::string key(ks, len);
            if (!baseline_keys.count(key)) {
                JsonVal val;
                std::set<const void*> seen;
                if (lua_to_json(T, -1, val, seen, 0)) {
                    next_bindings.obj[key] = std::move(val);
                } else {
                    lost.push_back(key);
                }
            }
        }
        lua_pop(T, 1);
    }
    // Also merge updates on `session` table if present.
    lua_getglobal(T, "session");
    if (lua_istable(T, -1)) {
        lua_pushnil(T);
        while (lua_next(T, -2) != 0) {
            if (lua_type(T, -2) == LUA_TSTRING) {
                size_t len = 0;
                const char* ks = lua_tolstring(T, -2, &len);
                std::string key(ks, len);
                JsonVal val;
                std::set<const void*> seen;
                if (lua_to_json(T, -1, val, seen, 0)) {
                    next_bindings.obj[key] = std::move(val);
                } else {
                    lost.push_back("session." + key);
                }
            }
            lua_pop(T, 1);
        }
    }
    lua_pop(T, 1);

    e->bindings = std::move(next_bindings);
    e->last_lost = lost;

    JsonVal resp = JsonVal::make_obj();
    JsonVal logs_arr = JsonVal::make_arr();
    for (const auto& l : ev.logs) logs_arr.arr.push_back(JsonVal::make_str(l));
    resp.obj["logs"] = std::move(logs_arr);
    resp.obj["bindings"] = e->bindings;
    JsonVal lost_arr = JsonVal::make_arr();
    for (const auto& l : lost) lost_arr.arr.push_back(JsonVal::make_str(l));
    resp.obj["lost_bindings"] = lost_arr;
    resp.obj["compile_ns"] = JsonVal::make_num(static_cast<double>(compile_ns));
    resp.obj["compute_ns"] = JsonVal::make_num(static_cast<double>(compute_ns));

    if (ev.killed.load(std::memory_order_relaxed) || ev.timed_out.load(std::memory_order_relaxed)) {
        resp.obj["status"] = JsonVal::make_str("killed");
        JsonVal eo = JsonVal::make_obj();
        eo.obj["kind"] = JsonVal::make_str(ev.timed_out.load() ? "TimeoutExceeded" : "Killed");
        eo.obj["message"] = JsonVal::make_str(ev.timed_out.load() ? "compute budget exceeded" : "engine killed");
        resp.obj["error"] = std::move(eo);
        lua_close(L);
        return alloc_c_str(dump_json(resp));
    }

    if (resume_rc == LUA_YIELD || resume_rc == LUA_BREAK || ev.cut_started.load() || ev.cut_requested.load()) {
        resp.obj["status"] = JsonVal::make_str("cut");
        JsonVal cut = JsonVal::make_obj();
        std::string reason = ev.cut_started.load() ? "started" : ev.cut_reason;
        cut.obj["reason"] = JsonVal::make_str(reason);
        cut.obj["lost_bindings"] = lost_arr;
        resp.obj["cut"] = std::move(cut);
        lua_close(L);
        return alloc_c_str(dump_json(resp));
    }

    if (resume_rc == LUA_OK) {
        resp.obj["status"] = JsonVal::make_str("completed");
        if (lua_gettop(T) >= 1) {
            JsonVal outv = JsonVal::make_null();
            std::set<const void*> seen;
            if (lua_to_json(T, -1, outv, seen, 0)) {
                resp.obj["value"] = std::move(outv);
            } else {
                resp.obj["status"] = JsonVal::make_str("failed");
                JsonVal eo = JsonVal::make_obj();
                eo.obj["kind"] = JsonVal::make_str("InvalidDataValue");
                eo.obj["message"] = JsonVal::make_str("cell return value is not JSON-serializable");
                resp.obj["error"] = std::move(eo);
            }
        } else {
            resp.obj["value"] = JsonVal::make_null();
        }
        lua_close(L);
        return alloc_c_str(dump_json(resp));
    }

    std::string err = lua_tostring(T, -1) ? lua_tostring(T, -1) : "runtime error";
    std::string kind = "ExecutionFailure";
    auto colon = err.find(": ");
    if (colon != std::string::npos && colon > 0 && colon < 40) {
        std::string prefix = err.substr(0, colon);
        bool alpha = true;
        for (char ch : prefix) {
            if (!std::isalnum(static_cast<unsigned char>(ch)) && ch != '_') { alpha = false; break; }
        }
        if (alpha) {
            kind = prefix;
            err = err.substr(colon + 2);
        }
    }
    resp.obj["status"] = JsonVal::make_str("failed");
    JsonVal eo = JsonVal::make_obj();
    eo.obj["kind"] = JsonVal::make_str(kind);
    eo.obj["message"] = JsonVal::make_str(err);
    resp.obj["error"] = std::move(eo);
    lua_close(L);
    return alloc_c_str(dump_json(resp));
}

char* kp_restore(kp_engine* e, const char* snap_json) {
    if (!e || !snap_json) {
        return alloc_c_str(R"({"ok":false,"error":"null pointer"})");
    }
    JsonVal v;
    JsonParser p(snap_json);
    if (!p.parse(v)) {
        return alloc_c_str(R"({"ok":false,"error":"invalid snapshot json"})");
    }
    if (const JsonVal* b = v.get("bindings")) {
        if (b->kind == JsonVal::JObj) {
            e->bindings = *b;
        }
    }
    if (const JsonVal* c = v.get("cut")) {
        e->last_cut = *c;
    } else {
        e->last_cut = JsonVal::make_null();
    }
    return alloc_c_str(R"({"ok":true})");
}

char* kp_snapshot(kp_engine* e) {
    if (!e) {
        return alloc_c_str(R"({"error":"null engine"})");
    }
    JsonVal out = JsonVal::make_obj();
    out.obj["format"] = JsonVal::make_num(2);
    out.obj["engine"] = JsonVal::make_str("luau");
    out.obj["build_id"] = JsonVal::make_str("luau-cpp");
    out.obj["bindings"] = e->bindings;
    out.obj["cut"] = e->last_cut;
    JsonVal lost_arr = JsonVal::make_arr();
    for (const auto& l : e->last_lost) lost_arr.arr.push_back(JsonVal::make_str(l));
    out.obj["lost_bindings"] = std::move(lost_arr);
    return alloc_c_str(dump_json(out));
}

char* kp_cut(kp_engine* e, const char* reason) {
    if (!e) return alloc_c_str(R"({"ok":false,"error":"null engine"})");
    std::string r = reason ? reason : "waiting";
    ActiveEval* ev = e->active.load(std::memory_order_acquire);
    if (!ev) {
        JsonVal out = JsonVal::make_obj();
        out.obj["ok"] = JsonVal::make_bool(true);
        out.obj["reason"] = JsonVal::make_str(r);
        out.obj["idle"] = JsonVal::make_bool(true);
        return alloc_c_str(dump_json(out));
    }
    ev->cut_reason = r;
    ev->cut_requested.store(true, std::memory_order_release);
    JsonVal out = JsonVal::make_obj();
    out.obj["ok"] = JsonVal::make_bool(true);
    out.obj["reason"] = JsonVal::make_str(r);
    return alloc_c_str(dump_json(out));
}

void kp_kill(kp_engine* e) {
    if (!e) return;
    ActiveEval* ev = e->active.load(std::memory_order_acquire);
    if (ev) {
        ev->killed.store(true, std::memory_order_release);
    }
}

} // extern "C"
