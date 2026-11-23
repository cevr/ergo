//go:build linux

package mygo

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// accessNode is one accessibility node as a comparison reads it, on either
// side: Android's widget class, the name a screen reader speaks and the
// states it announces. Boxes are not compared: the phone lays the view out
// at its own size, the headless window at the example's.
type accessNode struct {
	class, name                 string
	checked, disabled, selected bool
}

func (n accessNode) String() string {
	s := n.class + " " + fmt.Sprintf("%q", n.name)
	for _, f := range []struct {
		on   bool
		name string
	}{{n.checked, "checked"}, {n.disabled, "disabled"}, {n.selected, "selected"}} {
		if f.on {
			s += " " + f.name
		}
	}
	return s
}

// androidClass is the widget class MyGo's Android host gives a node of the
// WAI-ARIA role (screen.ariaRoles to MyGo's role, then SurfaceWindow.kt's
// Node.className): the reference the phone's tree is held to.
func androidClass(role string) string {
	switch role {
	case "button", "tab", "menuitem":
		return "android.widget.Button"
	case "checkbox":
		return "android.widget.CheckBox"
	case "radio":
		return "android.widget.RadioButton"
	case "switch":
		return "android.widget.Switch"
	case "slider":
		return "android.widget.SeekBar"
	case "textbox", "searchbox":
		return "android.widget.EditText"
	case "image":
		return "android.widget.ImageView"
	}
	return "android.view.View"
}

// wantAccess is the tree the window binary prints after script
// (-headless -tree, std ui.TreeOf: every node but a Generic one), as the
// phone's accessibility tree should show it: a nameless group is dropped,
// as MyGo's Android host drops it.
func wantAccess(t *testing.T, desktop, script string) []accessNode {
	t.Helper()
	out, err := play(desktop, script, "-headless", "-tree")
	if err != nil {
		t.Fatalf("%s -headless -tree: %v\n%s", desktop, err, out)
	}
	var nodes []accessNode
	for _, l := range strings.Split(out, "\n") {
		if !strings.HasPrefix(l, "{") {
			continue
		}
		var e struct {
			Role, Name string
			States     []string
		}
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("tree line %q: %v", l, err)
		}
		if androidDrops(e.Role, e.Name, e.States) {
			continue
		}
		nodes = append(nodes, accessNode{class: androidClass(e.Role), name: e.Name,
			checked: slices.Contains(e.States, "checked"), disabled: slices.Contains(e.States, "disabled"), selected: slices.Contains(e.States, "selected")})
	}
	return nodes
}

// androidDrops reports whether MyGo's Android host leaves a node out of the
// phone's tree: a node of MyGo's group role (WAI-ARIA group, generic or a
// role screen.ariaRoles does not name) with no label and no value
// (SurfaceWindow.kt, Node.decode). std ui writes a node's value among its
// states as "value=V" (ui.StatesOf), so the reference sees it.
func androidDrops(role, name string, states []string) bool {
	if name != "" || slices.ContainsFunc(states, func(s string) bool { return strings.HasPrefix(s, "value=") }) {
		return false
	}
	switch role {
	case "group", "generic":
		return true
	}
	return !knownRoles[role]
}

// knownRoles are the WAI-ARIA roles screen.ariaRoles maps to a MyGo role
// other than its group.
var knownRoles = map[string]bool{
	"button": true, "checkbox": true, "switch": true, "textbox": true, "searchbox": true, "static": true,
	"region": true, "image": true, "list": true, "listitem": true, "status": true, "tab": true, "tablist": true,
	"tabpanel": true, "dialog": true, "menu": true, "menuitem": true, "menuitemcheckbox": true, "tree": true,
	"treeitem": true, "heading": true, "alert": true, "link": true, "table": true, "row": true, "cell": true,
	"gridcell": true, "columnheader": true, "rowheader": true, "grid": true, "radiogroup": true, "radio": true,
	"listbox": true, "option": true, "slider": true, "spinbutton": true, "combobox": true, "navigation": true,
	"separator": true, "tooltip": true,
}

// uiNode is a node of `uiautomator dump`'s XML.
type uiNode struct {
	Class    string   `xml:"class,attr"`
	Package  string   `xml:"package,attr"`
	Text     string   `xml:"text,attr"`
	Hint     string   `xml:"hint,attr"`
	Checked  string   `xml:"checked,attr"`
	Enabled  string   `xml:"enabled,attr"`
	Selected string   `xml:"selected,attr"`
	Nodes    []uiNode `xml:"node"`
}

// phoneAccess is the app's accessibility tree on the phone: the virtual
// children of MyGo's surface view (the node of pkg whose class is in
// dev.mygo), read with uiautomator, which turns the accessibility on.
// The first dumps may come before the app published its tree; each dump
// is a round trip to the device, and the test's deadline stops an app
// that never publishes one.
func (e *emulator) phoneAccess(t *testing.T, pkg string) []accessNode {
	t.Helper()
	for {
		e.adb(t, "shell", "uiautomator", "dump", "/data/local/tmp/ergo-ui.xml")
		raw := e.adb(t, "exec-out", "cat", "/data/local/tmp/ergo-ui.xml")
		var root struct {
			Nodes []uiNode `xml:"node"`
		}
		if err := xml.Unmarshal(raw, &root); err != nil {
			t.Fatalf("uiautomator dump: %v\n%s", err, tail(raw))
		}
		if surface := findSurface(root.Nodes, pkg); surface != nil && len(surface.Nodes) > 0 {
			var out []accessNode
			for _, n := range surface.Nodes {
				name := n.Text
				if n.Class == "android.widget.EditText" && n.Hint != "" {
					name = n.Hint // a field's name is its hint; its text is its value
				}
				out = append(out, accessNode{class: n.Class, name: name, checked: n.Checked == "true", disabled: n.Enabled == "false", selected: n.Selected == "true"})
			}
			return out
		}
	}
}

func findSurface(nodes []uiNode, pkg string) *uiNode {
	for i := range nodes {
		n := &nodes[i]
		if n.Package == pkg && strings.HasPrefix(n.Class, "dev.mygo.") {
			return n
		}
		if s := findSurface(n.Nodes, pkg); s != nil {
			return s
		}
	}
	return nil
}

// sameAccess reports how two trees differ as multisets of nodes ("" when
// they agree): the host publishes its nodes in paint order, std ui's tree
// is in tree order, and neither order is the contract.
func sameAccess(want, got []accessNode) string {
	count := map[accessNode]int{}
	for _, n := range want {
		count[n]++
	}
	for _, n := range got {
		count[n]--
	}
	var diff []string
	for n, c := range count {
		switch {
		case c > 0:
			diff = append(diff, fmt.Sprintf("missing on the phone ×%d: %s", c, n))
		case c < 0:
			diff = append(diff, fmt.Sprintf("only on the phone ×%d: %s", -c, n))
		}
	}
	slices.Sort(diff)
	return strings.Join(diff, "\n")
}
