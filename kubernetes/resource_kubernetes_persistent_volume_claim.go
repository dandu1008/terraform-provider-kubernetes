package kubernetes

import (
	"fmt"
	"log"
	"time"

	"github.com/hashicorp/terraform/helper/resource"
	"github.com/hashicorp/terraform/helper/schema"
	api "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	pkgApi "k8s.io/apimachinery/pkg/types"
)

func resourceKubernetesPersistentVolumeClaim() *schema.Resource {
	return &schema.Resource{
		Create: resourceKubernetesPersistentVolumeClaimCreate,
		Read:   resourceKubernetesPersistentVolumeClaimRead,
		Exists: resourceKubernetesPersistentVolumeClaimExists,
		Update: resourceKubernetesPersistentVolumeClaimUpdate,
		Delete: resourceKubernetesPersistentVolumeClaimDelete,
		Importer: &schema.ResourceImporter{
			State: func(d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
				d.Set("wait_until_bound", true)
				return []*schema.ResourceData{d}, nil
			},
		},

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(5 * time.Minute),
		},

		Schema: persistentVolumeClaimSpecFields(false),
	}
}

func resourceKubernetesPersistentVolumeClaimCreate(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*kubernetesProvider).conn

	metadata := expandMetadata(d.Get("metadata").([]interface{}))
	spec, err := expandPersistentVolumeClaimSpec(d.Get("spec").([]interface{}))
	if err != nil {
		return err
	}

	claim := api.PersistentVolumeClaim{
		ObjectMeta: metadata,
		Spec:       spec,
	}

	log.Printf("[INFO] Creating new persistent volume claim: %#v", claim)
	out, err := conn.CoreV1().PersistentVolumeClaims(metadata.Namespace).Create(&claim)
	if err != nil {
		return err
	}
	log.Printf("[INFO] Submitted new persistent volume claim: %#v", out)

	d.SetId(buildId(out.ObjectMeta))
	name := out.ObjectMeta.Name

	if d.Get("wait_until_bound").(bool) {
		stateConf := &resource.StateChangeConf{
			Target:  []string{"Bound"},
			Pending: []string{"Pending"},
			Timeout: d.Timeout(schema.TimeoutCreate),
			Refresh: func() (interface{}, string, error) {
				out, err := conn.CoreV1().PersistentVolumeClaims(metadata.Namespace).Get(name, meta_v1.GetOptions{})
				if err != nil {
					log.Printf("[ERROR] Received error: %#v", err)
					return out, "", err
				}

				statusPhase := fmt.Sprintf("%v", out.Status.Phase)
				log.Printf("[DEBUG] Persistent volume claim %s status received: %#v", out.Name, statusPhase)
				return out, statusPhase, nil
			},
		}
		_, err = stateConf.WaitForState()
		if err != nil {
			var lastWarnings []api.Event
			var wErr error

			lastWarnings, wErr = getLastWarningsForObject(conn, out.ObjectMeta, "PersistentVolumeClaim", 3)
			if wErr != nil {
				return wErr
			}

			if len(lastWarnings) == 0 {
				lastWarnings, wErr = getLastWarningsForObject(conn, meta_v1.ObjectMeta{
					Name: out.Spec.VolumeName,
				}, "PersistentVolume", 3)
				if wErr != nil {
					return wErr
				}
			}

			return fmt.Errorf("%s%s", err, stringifyEvents(lastWarnings))
		}
	}
	log.Printf("[INFO] Persistent volume claim %s created", out.Name)

	return resourceKubernetesPersistentVolumeClaimRead(d, meta)
}

func resourceKubernetesPersistentVolumeClaimRead(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*kubernetesProvider).conn

	namespace, name, err := idParts(d.Id())
	if err != nil {
		return err
	}

	log.Printf("[INFO] Reading persistent volume claim %s", name)
	claim, err := conn.CoreV1().PersistentVolumeClaims(namespace).Get(name, meta_v1.GetOptions{})
	if err != nil {
		log.Printf("[DEBUG] Received error: %#v", err)
		return err
	}
	log.Printf("[INFO] Received persistent volume claim: %#v", claim)
	err = d.Set("metadata", flattenMetadata(claim.ObjectMeta, d))
	if err != nil {
		return err
	}
	err = d.Set("spec", flattenPersistentVolumeClaimSpec(claim.Spec))
	if err != nil {
		return err
	}

	return nil
}

func resourceKubernetesPersistentVolumeClaimUpdate(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*kubernetesProvider).conn

	namespace, name, err := idParts(d.Id())
	if err != nil {
		return err
	}

	ops := patchMetadata("metadata.0.", "/metadata/", d)
	// The whole spec is ForceNew = nothing to update there
	data, err := ops.MarshalJSON()
	if err != nil {
		return fmt.Errorf("Failed to marshal update operations: %s", err)
	}

	log.Printf("[INFO] Updating persistent volume claim: %s", ops)
	out, err := conn.CoreV1().PersistentVolumeClaims(namespace).Patch(name, pkgApi.JSONPatchType, data)
	if err != nil {
		return err
	}
	log.Printf("[INFO] Submitted updated persistent volume claim: %#v", out)

	return resourceKubernetesPersistentVolumeClaimRead(d, meta)
}

func resourceKubernetesPersistentVolumeClaimDelete(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*kubernetesProvider).conn

	namespace, name, err := idParts(d.Id())
	if err != nil {
		return err
	}

	log.Printf("[INFO] Deleting persistent volume claim: %#v", name)
	err = conn.CoreV1().PersistentVolumeClaims(namespace).Delete(name, &meta_v1.DeleteOptions{})
	if err != nil {
		return err
	}

	log.Printf("[INFO] Persistent volume claim %s deleted", name)

	d.SetId("")
	return nil
}

func resourceKubernetesPersistentVolumeClaimExists(d *schema.ResourceData, meta interface{}) (bool, error) {
	conn := meta.(*kubernetesProvider).conn

	namespace, name, err := idParts(d.Id())
	if err != nil {
		return false, err
	}

	log.Printf("[INFO] Checking persistent volume claim %s", name)
	_, err = conn.CoreV1().PersistentVolumeClaims(namespace).Get(name, meta_v1.GetOptions{})
	if err != nil {
		if statusErr, ok := err.(*errors.StatusError); ok && statusErr.ErrStatus.Code == 404 {
			return false, nil
		}
		log.Printf("[DEBUG] Received error: %#v", err)
	}
	return true, err
}
