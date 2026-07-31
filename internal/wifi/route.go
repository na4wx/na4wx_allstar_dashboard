package wifi

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// defaultRouteExists reports whether the kernel currently has any
// default route -- the semantically correct definition of "is there an
// active network connection" for Manager's watchdog. Checking "does
// some interface merely have an IP" instead would be wrong: wlan0 in
// hotspot mode has a (self-assigned) IP too, which would make the
// watchdog think it was already connected and never actually recover.
func defaultRouteExists(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out, stderr bytes.Buffer
	c := exec.CommandContext(ctx, "ip", "route", "show", "default")
	c.Stdout = &out
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return false, err
	}
	return strings.TrimSpace(out.String()) != "", nil
}
