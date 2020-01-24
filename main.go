package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/plugin"
	"github.com/hashicorp/terraform-plugin-sdk/terraform"
)

type config struct {
	kubeconfig        string
	kubeconfigContent string
	kubeconfigContext string
	kubectlPath       string
	kubectlToken      string
	kubectlValidate   bool
}

func main() {
	plugin.Serve(&plugin.ServeOpts{
		ProviderFunc: func() terraform.ResourceProvider {
			return &schema.Provider{
				Schema: map[string]*schema.Schema{
					"kubeconfig": &schema.Schema{
						Type:     schema.TypeString,
						Optional: true,
					},
					"kubeconfig_content": &schema.Schema{
						Type:     schema.TypeString,
						Optional: true,
					},
					"kubeconfig_context": &schema.Schema{
						Type:     schema.TypeString,
						Optional: true,
					},
					"kubectl_path": &schema.Schema{
						Type:     schema.TypeString,
						Optional: true,
					},
					"kubectl_token": &schema.Schema{
						Type:     schema.TypeString,
						Optional: true,
					},
					"kubectl_validate": &schema.Schema{
						Type:     schema.TypeBool,
						Optional: true,
						Default:  true,
					},
				},
				ResourcesMap: map[string]*schema.Resource{
					"k8s_manifest": resourceManifest(),
				},
				ConfigureFunc: func(d *schema.ResourceData) (interface{}, error) {
					return &config{
						kubeconfig:        d.Get("kubeconfig").(string),
						kubeconfigContent: d.Get("kubeconfig_content").(string),
						kubeconfigContext: d.Get("kubeconfig_context").(string),
						kubectlPath:       d.Get("kubectl_path").(string),
						kubectlToken:      d.Get("kubectl_token").(string),
						kubectlValidate:   d.Get("kubectl_validate").(bool),
					}, nil
				},
			}
		},
	})
}

func resourceManifest() *schema.Resource {
	return &schema.Resource{
		Create: resourceManifestCreate,
		Read:   resourceManifestRead,
		Update: resourceManifestUpdate,
		Delete: resourceManifestDelete,

		Schema: map[string]*schema.Schema{
			"namespace": &schema.Schema{
				Type:      schema.TypeString,
				Optional:  true,
				Sensitive: false,
				ForceNew:  true,
			},
			"content": &schema.Schema{
				Type:      schema.TypeString,
				Required:  true,
				Sensitive: false,
			},
		},
	}
}

func run(cmd *exec.Cmd) error {
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		cmdStr := cmd.Path + " " + strings.Join(cmd.Args, " ")
		if stderr.Len() == 0 {
			return fmt.Errorf("%s: %v", cmdStr, err)
		}
		return fmt.Errorf("%s %v: %s", cmdStr, err, stderr.Bytes())
	}
	return nil
}

func kubeconfigPath(m interface{}) (string, func(), error) {
	kubeconfig := m.(*config).kubeconfig
	kubeconfigContent := m.(*config).kubeconfigContent
	var cleanupFunc = func() {}

	if kubeconfig != "" && kubeconfigContent != "" {
		return kubeconfig, cleanupFunc, fmt.Errorf("both kubeconfig and kubeconfig_content are defined, " +
			"please use only one of the paramters")
	} else if kubeconfigContent != "" {
		tmpfile, err := ioutil.TempFile("", "kubeconfig_")
		if err != nil {
			defer cleanupFunc()
			return "", cleanupFunc, fmt.Errorf("creating a kubeconfig file: %v", err)
		}

		cleanupFunc = func() { os.Remove(tmpfile.Name()) }

		if _, err = io.WriteString(tmpfile, kubeconfigContent); err != nil {
			defer cleanupFunc()
			return "", cleanupFunc, fmt.Errorf("writing kubeconfig to file: %v", err)
		}
		if err = tmpfile.Close(); err != nil {
			defer cleanupFunc()
			return "", cleanupFunc, fmt.Errorf("completion of write to kubeconfig file: %v", err)
		}

		kubeconfig = tmpfile.Name()
	}

	if kubeconfig != "" {
		return kubeconfig, cleanupFunc, nil
	}

	return "", cleanupFunc, nil
}

func kubectlValidate(m interface{}) string {
	return fmt.Sprintf("--validate=%t", m.(*config).kubectlValidate)
}

func kubectl(m interface{}, kubeconfig string, args ...string) *exec.Cmd {
	if kubeconfig != "" {
		args = append([]string{"--kubeconfig", kubeconfig}, args...)
	}

	context := m.(*config).kubeconfigContext
	path := m.(*config).kubectlPath
	token := m.(*config).kubectlToken

	if path == "" {
		path = "kubectl"
	}

	if context != "" {
		args = append([]string{"--context", context}, args...)
	}

	if token != "" {
		args = append([]string{"--token", token}, args...)
	}

	return exec.Command(path, args...)
}

func resourceManifestCreate(d *schema.ResourceData, m interface{}) error {
	kubeconfig, cleanup, err := kubeconfigPath(m)
	if err != nil {
		return fmt.Errorf("determining kubeconfig: %v", err)
	}
	defer cleanup()

	namespace, isNamespace := d.GetOk("namespace")

	var cmd *exec.Cmd

	err = resource.Retry(d.Timeout(schema.TimeoutCreate), func() *resource.RetryError {
		if isNamespace {
			cmd = kubectl(m, kubeconfig, "apply", kubectlValidate(m), "-n", namespace.(string), "-f", "-")
		} else {
			cmd = kubectl(m, kubeconfig, "apply", kubectlValidate(m), "-f", "-")
		}
		cmd.Stdin = strings.NewReader(d.Get("content").(string))
		if err := run(cmd); err != nil {
			return resource.RetryableError(err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	var stdout *bytes.Buffer
	err = resource.Retry(d.Timeout(schema.TimeoutCreate), func() *resource.RetryError {
		stdout = &bytes.Buffer{}
		if isNamespace {
			cmd = kubectl(m, kubeconfig, "get", "-o", "json", "-n", namespace.(string), "-f", "-")
		} else {
			cmd = kubectl(m, kubeconfig, "get", "-o", "json", "-f", "-")
		}
		cmd.Stdin = strings.NewReader(d.Get("content").(string))
		cmd.Stdout = stdout
		if err := run(cmd); err != nil {
			return resource.RetryableError(err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	var data struct {
		Items []struct {
			Metadata struct {
				Selflink string `json:"selflink"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &data); err != nil {
		return fmt.Errorf("decoding response: %v", err)
	}
	if len(data.Items) != 1 {
		return fmt.Errorf("expected to create 1 resource, got %d", len(data.Items))
	}
	selflink := data.Items[0].Metadata.Selflink
	if selflink == "" {
		return fmt.Errorf("could not parse self-link from response %s", stdout.String())
	}
	d.SetId(selflink)
	return nil
}

func resourceManifestUpdate(d *schema.ResourceData, m interface{}) error {
	kubeconfig, cleanup, err := kubeconfigPath(m)
	if err != nil {
		return fmt.Errorf("determining kubeconfig: %v", err)
	}
	defer cleanup()

	return resource.Retry(d.Timeout(schema.TimeoutUpdate), func() *resource.RetryError {
		cmd := kubectl(m, kubeconfig, "apply", kubectlValidate(m), "-f", "-")
		cmd.Stdin = strings.NewReader(d.Get("content").(string))
		if err := run(cmd); err != nil {
			return resource.RetryableError(err)
		}
		return nil
	})
}

func resourceFromSelflink(s string) (resource, namespace string, ok bool) {
	parts := strings.Split(s, "/")
	if len(parts) < 2 {
		return "", "", false
	}
	resource = parts[len(parts)-2] + "/" + parts[len(parts)-1]

	for i, part := range parts {
		if part == "namespaces" && len(parts) > i+1 {
			namespace = parts[i+1]
			break
		}
	}
	return resource, namespace, true
}

func resourceManifestDelete(d *schema.ResourceData, m interface{}) error {
	k8sResource, namespace, ok := resourceFromSelflink(d.Id())
	if !ok {
		return fmt.Errorf("invalid resource id: %s", d.Id())
	}
	args := []string{"delete", k8sResource}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	kubeconfig, cleanup, err := kubeconfigPath(m)
	if err != nil {
		return fmt.Errorf("determining kubeconfig: %v", err)
	}
	defer cleanup()

	return resource.Retry(d.Timeout(schema.TimeoutDelete), func() *resource.RetryError {
		cmd := kubectl(m, kubeconfig, args...)
		if err := run(cmd); err != nil {
			return resource.RetryableError(err)
		}
		return nil
	})
}

func resourceManifestRead(d *schema.ResourceData, m interface{}) error {
	k8sResource, namespace, ok := resourceFromSelflink(d.Id())
	if !ok {
		return fmt.Errorf("invalid resource id: %s", d.Id())
	}

	args := []string{"get", "--ignore-not-found", k8sResource}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}

	var stdout *bytes.Buffer
	kubeconfig, cleanup, err := kubeconfigPath(m)
	if err != nil {
		return fmt.Errorf("determining kubeconfig: %v", err)
	}
	defer cleanup()

	err = resource.Retry(d.Timeout(schema.TimeoutRead), func() *resource.RetryError {
		cmd := kubectl(m, kubeconfig, args...)
		stdout = &bytes.Buffer{}
		cmd.Stdout = stdout
		if err := run(cmd); err != nil {
			return resource.RetryableError(err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	if strings.TrimSpace(stdout.String()) == "" {
		d.SetId("")
	}
	return nil
}
