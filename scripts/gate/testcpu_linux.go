package main

import (
	"os"
	"strconv"
)

// processCPU is the CPU seconds of the process pid and of the children it
// waited for (processCPUFrom).
func processCPU(pid int) (float64, error) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	return processCPUFrom(string(stat))
}
