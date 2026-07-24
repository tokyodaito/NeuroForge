package version

import (
	"fmt"
	"runtime"
)

var (
	Version = "0.0.0-dev"
	Commit  = "none"
	Date    = "unknown"
)

type Info struct {
	Version   string
	Commit    string
	Date      string
	GoVersion string
	OS        string
	Arch      string
}

func Current() Info {
	return Info{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
}

func (i Info) String() string {
	return fmt.Sprintf("forge %s\ncommit:   %s\nbuilt:    %s\ngo:       %s\nplatform: %s/%s\n",
		i.Version, i.Commit, i.Date, i.GoVersion, i.OS, i.Arch)
}
