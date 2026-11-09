package lower

// The program tables of this package (table_test.go): each test's fixed
// programs, built once per test process in one shared build. A test runs
// its program with tableExe or tableExes, by name.
var (
	_ = declarePrograms("TestGeneratorsReachEveryClass", testBuildProgram("generators", generatorProbes))
	_ = declarePrograms("TestLawRunB13", testBuildProgram("lawrun_b13", lawRunB13))
	_ = declarePrograms("TestStdLawsRun", testBuildProgram("stdlaws", stdLawsProbe))
	_ = declarePrograms("TestStdMachinesLawsRun", testBuildProgram("stdmachines", machinesLawsProbe))
	_ = declarePrograms("TestStdCSVLawsRun", testBuildProgram("stdcsv", stdCSVLawsProbe))
	_ = declarePrograms("TestQueuePollLaws", testBuildProgram("queuepolllaws", queuePollLawsProbe))
	_ = declarePrograms("TestStdRandLawsRun", testBuildProgram("stdrand", stdRandLawsProbe))
	_ = declarePrograms("TestStdSlicesLawsRun", testBuildProgram("stdslices", stdSlicesLawsProbe))
	_ = declarePrograms("TestSyncLaws", testBuildProgram("synclaws", syncLawsProbe))
	_ = declarePrograms("TestStdTimeLawsRun", testBuildProgram("stdtime", stdTimeLawsProbe))
	_ = declarePrograms("TestExecLaws", testBuildProgram("execlaws", execLawsProbe))
	_ = declarePrograms("TestFlagLaws", testBuildProgram("flaglaws", flagLawTests))
	_ = declarePrograms("TestIORecorderBehavesAsBefore", testBuildProgram("recorder", recorderProbe))
	_ = declarePrograms("TestStdAscendingMatchesGo", entryProgram("ascending", ascendingProgram))
	_ = declarePrograms("TestInputLinesMatchesModel", entryProgram("inputlines", inputLinesProgram))
	_ = declarePrograms("TestFSWritesRuns", entryProgram("fsWrites", fsWritesProgram))
	_ = declarePrograms("TestFSSafeRuns", entryProgram("fsSafe", fsSafeProgram))
	_ = declarePrograms("TestFSMemoryAgreesWithHost", entryProgram("fsMemory", fsMemoryProgram))
	_ = declarePrograms("TestTimeCivilMatchesGo", entryProgram("timeCivil", timeCivilProgram))
	_ = declarePrograms("TestHostAuthorityRuns", entryProgram("hostAuthority", hostAuthorityProgram))
	_ = declarePrograms("TestOSProcessRuns", entryProgram("osHost", osProgram), entryProgram("osFake", fakeProgram), entryProgram("osParam", paramProgram))
)
