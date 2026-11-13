package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// table prints an attr receipt as a Markdown table: instructions in
// millions and CPU (user+sys) in milliseconds, per stage.
func table(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: irspike table ATTR.json")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	var scs []scenario
	if err := json.Unmarshal(data, &scs); err != nil {
		return err
	}
	fmt.Println("| path | scenario | stage | runs | Minstr (user) | CPU ms |")
	fmt.Println("|---|---|---|---:|---:|---:|")
	for _, sc := range scs {
		for _, st := range sc.Stages {
			fmt.Printf("| %s | %s | %s | %d | %.1f | %.0f |\n", sc.Path, sc.Name, st.Name, st.Count,
				float64(st.Cost.Instructions)/1e6, float64(st.Cost.UserCPU+st.Cost.SysCPU)/1e6)
		}
		fmt.Printf("| %s | %s | (main.go %d B, ergoir.json %d B, %d IR funcs, %d fallback) | | | |\n", sc.Path, sc.Name, sc.GoBytes, sc.IRBytes, sc.IRFuncs, sc.Fallback)
	}
	return nil
}
