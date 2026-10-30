package main

import (
	"errors"
	"os"
)

func testMDLease(*os.File, bool) error { return errors.New("Markdown leases require Unix") }
