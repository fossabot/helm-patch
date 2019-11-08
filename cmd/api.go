package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/releaseutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

var (
	kind     string
	from     string
	to       string
	name     string
	revision int
)

type apiOptions struct {
	dryRun       bool
	kind         string
	from         string
	to           string
	resourceName string
	releaseName  string
	revision     int
}

type resourceInfo struct {
	apiVersion string
	kind       string
	name       string
}

func newAPICmd(out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "api [flags] RELEASE",
		Short: "path the api version of a resource",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return errors.New("name of release to be patched has to be defined")
			}
			return nil
		},

		RunE: runAPI,
	}

	flags := cmd.Flags()
	settings.AddFlags(flags)

	flags.StringVar(&kind, "kind", "", "the kind to patch the api version")
	flags.StringVar(&from, "from", "", "the api version that has to be replaced")
	flags.StringVar(&to, "to", "", "the api version to be set")
	flags.StringVar(&name, "name", "", "the name of the resource")
	flags.IntVar(&revision, "revision", -1, "the revision of the release to path")

	cmd.MarkFlagRequired("kind")
	cmd.MarkFlagRequired("to")

	return cmd

}

func runAPI(cmd *cobra.Command, args []string) error {

	apiOptions := apiOptions{
		dryRun:       settings.dryRun,
		kind:         kind,
		from:         from,
		to:           to,
		resourceName: name,
		releaseName:  args[0],
		revision:     revision,
	}
	return patchAPI(apiOptions)
}

func patchAPI(opts apiOptions) error {
	if opts.dryRun {
		log.Println("NOTE: This is in dry-run mode, the following actions will not be executed.")
		log.Println("Run without --dry-run to take the actions described below:")
		log.Println()
	}

	cfg := new(action.Configuration)

	if err := cfg.Init(
		settings.RESTClientGetter(),
		settings.Namespace(),
		os.Getenv("HELM_DRIVER"), debug); err != nil {
		return err
	}

	releases, err := cfg.Releases.List(opts.filter)
	if err != nil {
		return err
	}

	var rel *release.Release
	if len(releases) > 0 {
		rel = releases[len(releases)-1]
	}

	log.Printf("Processing release: '%s' with revision: %v\n", rel.Name, rel.Version)

	changed := false
	manifests := releaseutil.SplitManifests(rel.Manifest)

	for name, data := range manifests {
		resource := make(map[string]interface{})
		if err := yaml.Unmarshal([]byte(data), &resource); err != nil {
			return err
		}

		us := &unstructured.Unstructured{
			Object: resource,
		}

		if i := info(opts, us); i != nil {
			p, err := patchManifest(opts, resource, i)
			if err != nil {
				return err
			} else if p != "" {
				manifests[name] = p
				changed = true
			}
		}
	}

	if changed {
		if !opts.dryRun {
			err = saveResource(manifests, rel, cfg)
			if err != nil {
				return err
			}
		}
		log.Printf("Release: '%s' with revision: %v patched successfully\n", rel.Name, rel.Version)

	} else {
		log.Print("Nothing to patch")
	}
	return nil
}

func saveResource(manifests map[string]string, rel *release.Release, cfg *action.Configuration) error {
	b := bytes.NewBuffer(nil)
	for name, content := range manifests {
		if strings.TrimSpace(content) == "" {
			continue
		}
		fmt.Fprintf(b, "---\n# Source: %s\n%s\n", name, content)
	}
	rel.Manifest = b.String()
	return cfg.Releases.Update(rel)
}

func patchManifest(opts apiOptions, resource map[string]interface{}, i *resourceInfo) (string, error) {
	resource["apiVersion"] = opts.to
	log.Printf("Patching kind: %s name: %s from apiVersion: %s to apiVersion: %s\n", i.kind, i.name, i.apiVersion, opts.to)

	m, err := yaml.Marshal(resource)
	if err == nil {
		return string(m), nil
	}
	return "", nil
}

func info(opts apiOptions, resource interface{}) *resourceInfo {
	ro, ok := resource.(runtime.Object)
	if !ok {
		return nil
	}
	meta, ok := resource.(metav1.Object)
	if !ok {
		return nil
	}

	name := meta.GetName()
	if name == "" || (name != opts.resourceName && opts.resourceName != "") {
		return nil
	}

	k := ro.GetObjectKind().GroupVersionKind().Kind
	if k == "" || k != opts.kind {
		return nil
	}

	version := ro.GetObjectKind().GroupVersionKind().GroupVersion().String()
	if version == "" || (version != opts.from && opts.from != "") {
		return nil
	}

	return &resourceInfo{
		kind:       k,
		apiVersion: version,
		name:       name,
	}
}

func (opts *apiOptions) filter(rel *release.Release) bool {
	if rel == nil || rel.Name == "" || rel.Name != opts.releaseName {
		return false
	}

	if opts.revision > 0 {
		return rel.Version == opts.revision
	}
	return true
}
