package lower

import "testing"

func TestErgoRunE1GenericTaskClosure(t *testing.T) {
	needsBuild(t)
	wantRun(t, "import task\n"+nestedWitnessDecls+`func Child[T Valid](x T) bool {
	let t = task.Go() { All(x) }
	t.Wait()
}
func main() needs Console {
	Console.Println("\(Child(Card{N: 1})) \(Child(Card{N: 0}))")
}`, "true false\n")
}
