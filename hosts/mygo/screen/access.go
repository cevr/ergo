package screen

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/drawing/platform"
)

// The semantics of an accessibility node (UG2): each setter changes the
// node the last Access call added, by WAI-ARIA's names, as std ui's
// Access reads a node (ui.RoleName). AccessText reads a Headless window's
// last published tree back, by MyGo's own names, so a test holds the two
// ends apart.

// ariaRoles are the MyGo roles of WAI-ARIA's role names. A searchbox is a
// text field with AccessSearch (AccessRole).
var ariaRoles = map[string]platform.AccessRole{
	"button": platform.RoleButton, "checkbox": platform.RoleCheckBox, "switch": platform.RoleSwitch,
	"textbox": platform.RoleTextField, "searchbox": platform.RoleTextField, "static": platform.RoleText,
	"group": platform.RoleGroup, "region": platform.RoleRegion, "image": platform.RoleImage,
	"list": platform.RoleList, "listitem": platform.RoleListItem, "status": platform.RoleStatus,
	"generic": platform.RoleGroup, "tab": platform.RoleTab, "tablist": platform.RoleTabList,
	"tabpanel": platform.RoleTabPanel, "dialog": platform.RoleDialog, "menu": platform.RoleMenu,
	"menuitem": platform.RoleMenuItem, "menuitemcheckbox": platform.RoleMenuItemCheckBox,
	"tree": platform.RoleTree, "treeitem": platform.RoleTreeItem, "heading": platform.RoleHeading,
	"alert": platform.RoleAlert, "link": platform.RoleLink, "table": platform.RoleTable,
	"row": platform.RoleRow, "cell": platform.RoleCell, "gridcell": platform.RoleCell,
	"columnheader": platform.RoleColumnHeader, "rowheader": platform.RoleRowHeader,
	"grid": platform.RoleGrid, "radiogroup": platform.RoleRadioGroup, "radio": platform.RoleRadio,
	"listbox": platform.RoleList, "option": platform.RoleListItem, "slider": platform.RoleSlider,
	"spinbutton": platform.RoleStepper, "combobox": platform.RoleComboBox,
	"navigation": platform.RoleNavigation, "separator": platform.RoleSeparator,
	"tooltip": platform.RoleTooltip,
}

// ariaStates are the MyGo states of WAI-ARIA's state names.
var ariaStates = map[string]platform.AccessStates{
	"disabled": platform.AccessDisabled, "checked": platform.AccessChecked, "mixed": platform.AccessMixed,
	"selected": platform.AccessSelected, "expanded": platform.AccessExpanded, "busy": platform.AccessBusy,
	"invalid": platform.AccessInvalid, "multiselectable": platform.AccessMultiselectable,
}

var ariaPopups = map[string]platform.AccessPopup{
	"menu": platform.PopupMenu, "listbox": platform.PopupListBox, "tree": platform.PopupTree,
	"grid": platform.PopupGrid, "dialog": platform.PopupDialog,
}

var ariaCurrents = map[string]platform.AccessCurrent{
	"true": platform.CurrentItem, "page": platform.CurrentPage, "step": platform.CurrentStep,
	"location": platform.CurrentLocation, "date": platform.CurrentDate, "time": platform.CurrentTime,
}

// last runs f on the node the last Access call added.
func (w *Window) last(f func(n *platform.AccessNode)) {
	w.mu.Lock()
	if len(w.access) > 0 {
		f(&w.access[len(w.access)-1])
	}
	w.mu.Unlock()
}

// AccessRole gives the last node the WAI-ARIA role role; an unknown role
// leaves it a group.
func (w *Window) AccessRole(role string) {
	w.last(func(n *platform.AccessNode) {
		r, ok := ariaRoles[role]
		if !ok {
			r = platform.RoleGroup
		}
		n.Role = r
		if role == "searchbox" {
			n.States |= platform.AccessSearch
		}
	})
}

// AccessState sets the WAI-ARIA state state on the last node.
func (w *Window) AccessState(state string) {
	w.last(func(n *platform.AccessNode) { n.States |= ariaStates[state] })
}

// AccessFocusable makes the last node one the keyboard reaches.
func (w *Window) AccessFocusable() {
	w.last(func(n *platform.AccessNode) { n.States |= platform.AccessFocusable })
}

// AccessDescribe gives the last node its description, its heading or
// tree level (0: none), the item it is the current one of ("" for none,
// as aria-current), and the role of the popup it opens ("" for none).
func (w *Window) AccessDescribe(description string, level int, current string, popup string) {
	w.last(func(n *platform.AccessNode) {
		n.Description, n.Level = description, level
		n.Current, n.HasPopup = ariaCurrents[current], ariaPopups[popup]
	})
}

// AccessRange gives the last node a value in a range.
func (w *Window) AccessRange(now, min, max float64) {
	w.last(func(n *platform.AccessNode) { n.Now, n.Min, n.Max = now, min, max })
}

// AccessRelate adds a relation of the last node to the node of id to:
// "controls" or "describedby".
func (w *Window) AccessRelate(kind string, to int) {
	w.last(func(n *platform.AccessNode) {
		switch kind {
		case "controls":
			n.Controls = append(n.Controls, uint64(to))
		case "describedby":
			n.DescribedBy = append(n.DescribedBy, uint64(to))
		}
	})
}

// roleNames are MyGo's roles by their names in this package's text.
var roleNames = map[platform.AccessRole]string{
	platform.RoleGroup: "group", platform.RoleText: "text", platform.RoleButton: "button",
	platform.RoleLink: "link", platform.RoleCheckBox: "checkbox", platform.RoleRadio: "radio",
	platform.RoleSwitch: "switch", platform.RoleSlider: "slider", platform.RoleTextField: "textfield",
	platform.RoleImage: "image", platform.RoleList: "list", platform.RoleDialog: "dialog",
	platform.RoleTooltip: "tooltip", platform.RoleTabList: "tablist", platform.RoleTab: "tab",
	platform.RoleStatus: "status", platform.RoleTable: "table", platform.RoleRow: "row",
	platform.RoleCell: "cell", platform.RoleColumnHeader: "columnheader", platform.RoleTree: "tree",
	platform.RoleTreeItem: "treeitem", platform.RoleListItem: "listitem", platform.RoleRadioGroup: "radiogroup",
	platform.RoleToggleButton: "togglebutton", platform.RoleComboBox: "combobox", platform.RoleStepper: "stepper",
	platform.RoleMenu: "menu", platform.RoleMenuItem: "menuitem", platform.RoleMenuItemCheckBox: "menuitemcheckbox",
	platform.RoleHeading: "heading", platform.RoleRegion: "region", platform.RoleAlert: "alert",
	platform.RoleSeparator: "separator", platform.RoleRowHeader: "rowheader", platform.RoleNavigation: "navigation",
	platform.RoleGrid: "grid", platform.RoleTabPanel: "tabpanel",
}

// stateNames are MyGo's states in the order AccessText writes them.
var stateNames = []struct {
	s    platform.AccessStates
	name string
}{
	{platform.AccessDisabled, "disabled"}, {platform.AccessChecked, "checked"}, {platform.AccessMixed, "mixed"},
	{platform.AccessSelected, "selected"}, {platform.AccessExpanded, "expanded"}, {platform.AccessBusy, "busy"},
	{platform.AccessInvalid, "invalid"}, {platform.AccessMultiselectable, "multiselectable"}, {platform.AccessSearch, "search"},
}

// AccessText is the node of id in a Headless window's last published
// accessibility tree, by MyGo's names: its role, name, states, level,
// range, current item, popup, description and relations; "" when the
// tree has no such node.
func (w *Window) AccessText(id int) string {
	if w.fake == nil {
		return ""
	}
	t, _ := w.fake.Accessibility()
	if t == nil {
		return ""
	}
	for _, n := range t.Nodes {
		if n.ID != uint64(id) {
			continue
		}
		var b strings.Builder
		role, ok := roleNames[n.Role]
		if !ok {
			role = fmt.Sprintf("role%d", n.Role)
		}
		fmt.Fprintf(&b, "%s %q", role, n.Label)
		for _, s := range stateNames {
			if n.States&s.s != 0 {
				b.WriteString(" " + s.name)
			}
		}
		if n.Level != 0 {
			fmt.Fprintf(&b, " level %d", n.Level)
		}
		if n.Now != 0 || n.Min != 0 || n.Max != 0 {
			fmt.Fprintf(&b, " range %g %g %g", n.Now, n.Min, n.Max)
		}
		if n.Current != platform.CurrentNone {
			b.WriteString(" current " + n.Current.String())
		}
		if n.HasPopup != platform.PopupNone {
			b.WriteString(" popup " + n.HasPopup.String())
		}
		if n.Description != "" {
			fmt.Fprintf(&b, " description %q", n.Description)
		}
		for _, c := range n.Controls {
			fmt.Fprintf(&b, " controls %d", c)
		}
		for _, c := range n.DescribedBy {
			fmt.Fprintf(&b, " describedby %d", c)
		}
		return b.String()
	}
	return ""
}
