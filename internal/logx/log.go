// Package logx prints the wrapper's own lines, prefixed so they stand out from the Java logs.
package logx

import (
	"fmt"
	"os"
)

func Printf(format string, args ...any) {
	// One Write call per line so it does not interleave with the Java output copy.
	fmt.Fprintf(os.Stdout, "[sleepy] "+format+"\n", args...)
}
