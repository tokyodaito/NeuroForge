//go:build !unix

package cli

func sendSIGTERM(pid int) error {
	return nil
}
