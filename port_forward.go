package main

import (
	"fmt"
	"os"

	"github.com/hashicorp/terraform/helper/schema"
)

func dataSourcePortForward() *schema.Resource {
	return &schema.Resource{
		Read: dataSourcePortForwardRead,

		Schema: map[string]*schema.Schema{
			"name": &schema.Schema{
				Type:      schema.TypeString,
				Required:  true,
				Sensitive: false,
			},
			"namespace": &schema.Schema{
				Type:      schema.TypeString,
				Required:  true,
				Sensitive: false,
			},
			"port": &schema.Schema{
				Type:      schema.TypeInt,
				Required:  true,
				Sensitive: false,
			},
			"local_port": &schema.Schema{
				Type:      schema.TypeInt,
				Sensitive: false,
				Optional:  true,
			},
		},
	}
}

func dataSourcePortForwardRead(d *schema.ResourceData, m interface{}) error {

	namespace := d.Get("namespace").(string)
	name := d.Get("name").(string)
	port := d.Get("port").(int)

	d.SetId(fmt.Sprintf("%s.%s:%d", name, namespace, port))

	kubeconfig, cleanup, err := kubeconfigPath(m)
	if err != nil {
		return fmt.Errorf("determining kubeconfig: %v", err)
	}
	defer cleanup()

	cmd := kubectl(m, kubeconfig, "port-forward", "-n", namespace, "service/"+name, fmt.Sprint(port))

	go func() {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Run()
		cmd.Wait()
	}()

	return nil
}
