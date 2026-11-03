package syntax

// E2dCopy is the E2c round 1 review m3 control's syntax method: it yields a
// node, so a call to it, a value of it, or a conversion to an interface it
// implements is a construction outside made and clone. Today's syntax
// package has no such method but Parse; the test adds this one to its copy.
func (x *Ident) E2dCopy() *Ident {
	c := *x
	return &c
}
