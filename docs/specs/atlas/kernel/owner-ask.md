# Owner notes on the persistent cell kernel (verbatim, newest last)

*Historical source specification for the dotfiles TypeScript `kernel-wake` effort, incorporated into `atlas`'s built-in kernel design ([../spec.md](../spec.md)) and kernel engine probe ([../units.md](../units.md)).*

~00:55Z: "Why is the Ergo kernel in the workbox constantly underload? Like it's always working. It's always doing something. One of the rules that we have for the orchestrator that it is that it never is blocked."

~00:58Z: "Let's make sure that the orchestrator is never always stalled or busy."

~01:20Z: "Also, I think, let's research whether or not we've actually benefitted from our bun kernel over this, um, uh, psychoscript byte coat VM, right? Because maybe it's design is better suited for how we want to use an orchestrator. Like, have we benefitted particularly from having this kind of rebel?" (rebel = REPL)

~02:0xZ: "Wait, in prime agent, do subagents get on their own kernel? Should we not maybe have kernels or would that be too much memory?"

~02:0xZ: "Okay, well, let's design the kernel so it reduces weight as much as possible. In general, we want to be a, we want basically, um, wakes, we want things to wake us up rather than constantly be executing." ("weight" may be "wait": read it as both: as little waiting inside a cell as possible, and as light as possible)

Earlier standing words: orchestrators "do not block on the shell"; "Can we run requests in the background so that or in a sub-agent so that any requests don't stall the orchestrator"; "Can we make it so push-based so you don't have to pull"; "lets keep the 2 minute to background".

~02:0xZ (right after): "And then we can give it to our subagents."

~02:1xZ: "Also, maybe we can consolidate concepts. Does open codes, automations, replace our wakes and monitors?"

## Answers Thu ~02:25Z (verbatim)
q230 (wait bound for an orchestrator's cell): "2 minutes".
q231 (blocking question form in roots): "lwts make thenasyncnqueationnform the only one" (= let's make the async question form the only one: the blocking question tool goes, for every session, not only roots).
q232 (which role gets the kernel first): "each" (every role gets it; measured on each, not one role first).

~03:09Z: "Although I should say that Sometimes, you know, making 4 loops and well loops and what not in the kernel isn't a bad thing. I just suppose how do we balance, not blocking the thread, right?" ("4 loops and well loops" = for loops and while loops)

~04:15Z: "What's prioritize the kernel work? Um, maybe consolidate the lanes that we're working on the kernel differently and put them into one lane." (= let's prioritize the kernel work; put the kernel units into ONE lane)
