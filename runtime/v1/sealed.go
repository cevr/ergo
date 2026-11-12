package v1

// These markers implement the private operations of the runtime-backed
// std protocols. They carry no runtime state or dispatch. The source
// visibility rule, not an extern's result type, prevents user providers.
func (*Task[T]) runtimeOnly()        {}
func (*Queue[T]) runtimeOnly()       {}
func (*Ref[T]) runtimeOnly()         {}
func (*Int64) runtimeOnly()          {}
func (*Bool) runtimeOnly()           {}
func (*FS) runtimeOnly()             {}
func (*Text) runtimeOnly()           {}
func (*Cursor) runtimeOnly()         {}
func (*Actor[S, E, D]) runtimeOnly() {}
func (*Host[A]) runtimeOnly()        {}
func (*Set) runtimeOnly()            {}
func (*Op[A, F]) runtimeOnly()       {}
func (*Timeline) runtimeOnly()       {}
func (*Stream) runtimeOnly()         {}
func (*Redacted[T]) runtimeOnly()    {}
func (*Running) runtimeOnly()        {}
func (*Exchange) runtimeOnly()       {}
