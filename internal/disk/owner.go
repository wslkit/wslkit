package disk

import "strings"

// A disk that no distribution claims is not a disk that nobody wants. Docker
// Desktop's holds every volume the user has; podman's holds their machines; a
// distribution somebody exported and unregistered may be their only copy of it.
// Deleting one because nothing claimed it destroys data nothing warned about.
//
// The path says who put it there, and saying so turns a list of unfamiliar
// paths into a list a person can actually decide about.

// Owner names the software a disk belongs to, and what is in it.
type Owner struct {
	// Name is the application, or "" when the path says nothing.
	Name string
	// Holds is one line on what is inside, for a reader deciding whether to
	// delete it.
	Holds string
}

// owners are matched against the path, in order: the first match wins, so the
// more specific patterns come first.
var owners = []struct {
	match []string
	owner Owner
}{
	{[]string{`\docker\wsl\`, `\dockerdesktopwsl\`, `docker_data.vhdx`, `docker_desktop.vhdx`},
		Owner{"Docker Desktop", "every Docker volume, image and container on this machine"}},
	{[]string{`\rancher-desktop\`, `\rancherdesktop\`},
		Owner{"Rancher Desktop", "its Kubernetes cluster and container storage"}},
	{[]string{`\podman\`, `podman-machine`},
		Owner{"Podman", "a Podman machine and everything in it"}},
	{[]string{`\minikube\`},
		Owner{"minikube", "a Kubernetes cluster"}},
	{[]string{`\wslkit\trash\`, `\wsldisk\trash\`},
		Owner{"wslkit", "a distribution moved to the trash; wslkit disk undelete brings it back"}},
	{[]string{`\packages\`},
		Owner{"", "a distribution installed from the Store, which may have been unregistered without deleting its disk"}},
}

// IdentifyOwner works out who a loose disk belongs to from its path.
func IdentifyOwner(path string) Owner {
	p := strings.ToLower(path)
	for _, o := range owners {
		for _, m := range o.match {
			if strings.Contains(p, m) {
				return o.owner
			}
		}
	}
	return Owner{}
}

// Describe is the short label for a listing: the application where one is
// known, and an honest blank where it is not.
func (o Owner) Describe() string {
	if o.Name != "" {
		return o.Name
	}
	if o.Holds != "" {
		return "unknown"
	}
	return "unknown"
}
