package controller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
)

func TestBuildMSSQLConf_NoConfig(t *testing.T) {
	srv := &v1alpha1.SQLServer{
		Spec: v1alpha1.SQLServerSpec{
			Instance: &v1alpha1.InstanceSpec{},
		},
	}
	conf := buildMSSQLConf(srv)
	if conf != "" {
		t.Errorf("expected empty config, got %q", conf)
	}
}

func TestBuildMSSQLConf_RawConfig(t *testing.T) {
	raw := "[network]\nforceencryption = 1\n"
	srv := &v1alpha1.SQLServer{
		Spec: v1alpha1.SQLServerSpec{
			Instance: &v1alpha1.InstanceSpec{
				Config: &raw,
			},
		},
	}
	conf := buildMSSQLConf(srv)
	if conf != raw {
		t.Errorf("expected raw config passthrough, got %q", conf)
	}
}

func TestBuildMSSQLConf_AutoMemory(t *testing.T) {
	srv := &v1alpha1.SQLServer{
		Spec: v1alpha1.SQLServerSpec{
			Instance: &v1alpha1.InstanceSpec{
				Resources: &corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("4Gi"),
					},
				},
			},
		},
	}
	conf := buildMSSQLConf(srv)
	// 4Gi = 4096 MB, 80% = 3276
	if !strings.Contains(conf, "[memory]") {
		t.Error("expected [memory] section")
	}
	if !strings.Contains(conf, "memorylimitmb = 3276") {
		t.Errorf("expected memorylimitmb = 3276, got %q", conf)
	}
}

func TestBuildMSSQLConf_AutoMemoryWithExistingMemorySection(t *testing.T) {
	raw := "[memory]\nmaxservermemory = 2048\n"
	srv := &v1alpha1.SQLServer{
		Spec: v1alpha1.SQLServerSpec{
			Instance: &v1alpha1.InstanceSpec{
				Config: &raw,
				Resources: &corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("4Gi"),
					},
				},
			},
		},
	}
	conf := buildMSSQLConf(srv)
	// Should append memorylimitmb to existing [memory] section, not add a new one
	if strings.Count(conf, "[memory]") != 1 {
		t.Errorf("expected exactly one [memory] section, got:\n%s", conf)
	}
	if !strings.Contains(conf, "memorylimitmb = 3276") {
		t.Errorf("expected auto memorylimitmb, got:\n%s", conf)
	}
}

func TestBuildMSSQLConf_ExplicitMemoryNotOverridden(t *testing.T) {
	raw := "[memory]\nmemorylimitmb = 2048\n"
	srv := &v1alpha1.SQLServer{
		Spec: v1alpha1.SQLServerSpec{
			Instance: &v1alpha1.InstanceSpec{
				Config: &raw,
				Resources: &corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("4Gi"),
					},
				},
			},
		},
	}
	conf := buildMSSQLConf(srv)
	// User set memorylimitmb explicitly — should NOT be overridden
	if !strings.Contains(conf, "memorylimitmb = 2048") {
		t.Errorf("expected user memorylimitmb preserved, got:\n%s", conf)
	}
	if strings.Contains(conf, "memorylimitmb = 3276") {
		t.Error("auto memorylimitmb should not override user-specified value")
	}
}

func TestBuildMSSQLConf_NoMemoryLimitNoAutoCalc(t *testing.T) {
	raw := "[network]\nforceencryption = 1\n"
	srv := &v1alpha1.SQLServer{
		Spec: v1alpha1.SQLServerSpec{
			Instance: &v1alpha1.InstanceSpec{
				Config: &raw,
				// No resources.limits.memory
			},
		},
	}
	conf := buildMSSQLConf(srv)
	if strings.Contains(conf, "memorylimitmb") {
		t.Error("should not add memorylimitmb without memory limit")
	}
}

func TestBuildMSSQLConf_CompleteExample(t *testing.T) {
	raw := `[memory]
memorylimitmb = 4096

[network]
forceencryption = 1

[traceflag]
traceflag0 = 1222
traceflag1 = 3226
`
	srv := &v1alpha1.SQLServer{
		Spec: v1alpha1.SQLServerSpec{
			Instance: &v1alpha1.InstanceSpec{
				Config: &raw,
			},
		},
	}
	conf := buildMSSQLConf(srv)
	if conf != raw {
		t.Errorf("expected exact passthrough of complete config, got:\n%s", conf)
	}
}
