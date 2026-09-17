package main

import "os"

func osStderr() *os.File { return os.Stderr }
func osExit(code int)    { os.Exit(code) }
