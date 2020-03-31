package bpf

import (
	"bytes"
	"fmt"
	"regexp"

	"golang.org/x/sys/unix"

	"github.com/ti-mo/conntracct/pkg/kallsyms"
	"github.com/ti-mo/conntracct/pkg/kernel"
)

// kernelRelease returns the release name of the running kernel.
func kernelRelease() (string, error) {

	uname := unix.Utsname{}
	if err := unix.Uname(&uname); err != nil {
		return "", err
	}

	// Convert [65]byte to a string.
	release := string(uname.Release[:bytes.IndexByte(uname.Release[:], 0)])

	// Some distributions' (Fedora) kernel release strings are not correct
	// semantic versions, for example '4.20.3-200.fc29.x86_64'.
	// Extract the significant portion x.x(.x) without
	rgx := regexp.MustCompile(`^(\d{1,3}\.\d{1,3}(?:\.\d{1,3})?).*$`)
	out := rgx.FindStringSubmatch(release)
	if len(out) == 0 {
		return "", fmt.Errorf(errKernelRelease, release)
	}

	// Return the first capture group.
	return out[1], nil
}

// checkProbeKsyms checks whether a list of k(ret)probes have their target functions
// present in the kernel. Expects strings in the format of k(ret)probe/<kernel-symbol>.
func checkProbeKsyms(probes kernel.Probes) error {

	// Parse /proc/kallsyms and store result in kallsyms package.
	err := kallsyms.Refresh()
	if err != nil {
		return err
	}

	for _, p := range probes {
		sf, err := kallsyms.Find(p.Name)
		if err != nil {
			return err
		}

		if !sf {
			return fmt.Errorf(errFmtSymNotFound, p.Name)
		}
	}

	return nil
}
