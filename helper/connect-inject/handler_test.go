package connectinject

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mattbaird/jsonpatch"
	"github.com/stretchr/testify/require"
	"k8s.io/api/admission/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestHandlerHandle(t *testing.T) {
	basicSpec := corev1.PodSpec{
		Containers: []corev1.Container{
			corev1.Container{
				Name: "web",
			},
		},
	}

	cases := []struct {
		Name    string
		Handler Handler
		Req     v1beta1.AdmissionRequest
		Err     string // expected error string, not exact
		Patches []jsonpatch.JsonPatchOperation
	}{
		{
			"kube-system namespace",
			Handler{},
			v1beta1.AdmissionRequest{
				Object: encodeRaw(t, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: metav1.NamespaceSystem,
					},
				}),
			},
			"",
			nil,
		},

		{
			"already injected",
			Handler{},
			v1beta1.AdmissionRequest{
				Object: encodeRaw(t, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							annotationStatus: "injected",
						},
					},

					Spec: basicSpec,
				}),
			},
			"",
			nil,
		},

		{
			"empty pod",
			Handler{},
			v1beta1.AdmissionRequest{
				Object: encodeRaw(t, &corev1.Pod{
					Spec: basicSpec,
				}),
			},
			"",
			[]jsonpatch.JsonPatchOperation{
				{
					Operation: "add",
					Path:      "/spec/containers/-",
				},
				{
					Operation: "add",
					Path:      "/metadata/annotations/" + escapeJSONPointer(annotationStatus),
				},
			},
		},

		{
			"empty pod with injection disabled",
			Handler{},
			v1beta1.AdmissionRequest{
				Object: encodeRaw(t, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							annotationInject: "false",
						},
					},

					Spec: basicSpec,
				}),
			},
			"",
			nil,
		},

		{
			"empty pod with injection truthy",
			Handler{},
			v1beta1.AdmissionRequest{
				Object: encodeRaw(t, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							annotationInject: "t",
						},
					},

					Spec: basicSpec,
				}),
			},
			"",
			[]jsonpatch.JsonPatchOperation{
				{
					Operation: "add",
					Path:      "/spec/containers/-",
				},
				{
					Operation: "add",
					Path:      "/metadata/annotations/" + escapeJSONPointer(annotationStatus),
				},
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.Name, func(t *testing.T) {
			require := require.New(t)
			resp := tt.Handler.Mutate(&tt.Req)
			if (tt.Err == "") != resp.Allowed {
				t.Fatalf("allowed: %v, expected err: %v", resp.Allowed, tt.Err)
			}
			if tt.Err != "" {
				require.Contains(resp.Result.Message, tt.Err)
				return
			}

			var actual []jsonpatch.JsonPatchOperation
			if len(resp.Patch) > 0 {
				require.NoError(json.Unmarshal(resp.Patch, &actual))
				for i, _ := range actual {
					actual[i].Value = nil
				}
			}
			require.Equal(actual, tt.Patches)
		})
	}
}

// Test that an incorrect content type results in an error.
func TestHandlerHandle_badContentType(t *testing.T) {
	req, err := http.NewRequest("POST", "/", nil)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "text/plain")

	var h Handler
	rec := httptest.NewRecorder()
	h.Handle(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "content-type")
}

// Test that no body results in an error
func TestHandlerHandle_noBody(t *testing.T) {
	req, err := http.NewRequest("POST", "/", nil)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	var h Handler
	rec := httptest.NewRecorder()
	h.Handle(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "body")
}

func TestHandlerDefaultAnnotations(t *testing.T) {
	cases := []struct {
		Name     string
		Pod      *corev1.Pod
		Expected map[string]string
		Err      string
	}{
		{
			"empty",
			&corev1.Pod{},
			nil,
			"",
		},

		{
			"basic pod, no ports",
			&corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						corev1.Container{
							Name: "web",
						},

						corev1.Container{
							Name: "web-side",
						},
					},
				},
			},
			map[string]string{
				annotationService: "web",
			},
			"",
		},

		{
			"basic pod, name annotated",
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						annotationService: "foo",
					},
				},

				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						corev1.Container{
							Name: "web",
						},

						corev1.Container{
							Name: "web-side",
						},
					},
				},
			},
			map[string]string{
				annotationService: "foo",
			},
			"",
		},

		{
			"basic pod, with ports",
			&corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						corev1.Container{
							Name: "web",
							Ports: []corev1.ContainerPort{
								corev1.ContainerPort{
									Name:          "http",
									ContainerPort: 8080,
								},
							},
						},

						corev1.Container{
							Name: "web-side",
						},
					},
				},
			},
			map[string]string{
				annotationService: "web",
				annotationPort:    "http",
			},
			"",
		},
	}

	for _, tt := range cases {
		t.Run(tt.Name, func(t *testing.T) {
			require := require.New(t)

			var h Handler
			err := h.defaultAnnotations(tt.Pod)
			if (tt.Err != "") != (err != nil) {
				t.Fatalf("actual: %v, expected err: %v", err, tt.Err)
			}
			if tt.Err != "" {
				require.Contains(err.Error(), tt.Err)
				return
			}

			actual := tt.Pod.Annotations
			if len(actual) == 0 {
				actual = nil
			}
			require.Equal(actual, tt.Expected)
		})
	}
}

func TestHandlerContainerSidecar(t *testing.T) {
	minimal := func() *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					annotationService: "foo",
				},
			},

			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					corev1.Container{
						Name: "web",
					},

					corev1.Container{
						Name: "web-side",
					},
				},
			},
		}
	}

	cases := []struct {
		Name   string
		Pod    func(*corev1.Pod) *corev1.Pod
		Cmd    string // Strings.Contains test
		CmdNot string // Not contains
	}{
		{
			"Only service",
			func(pod *corev1.Pod) *corev1.Pod {
				pod.Annotations[annotationService] = "web"
				return pod
			},
			"-service=web",
			"-register",
		},

		{
			"Service port specified",
			func(pod *corev1.Pod) *corev1.Pod {
				pod.Annotations[annotationService] = "web"
				pod.Annotations[annotationPort] = "1234"
				return pod
			},
			"-service-addr=127.0.0.1:1234",
			"",
		},

		{
			"Upstream",
			func(pod *corev1.Pod) *corev1.Pod {
				pod.Annotations[annotationService] = "web"
				pod.Annotations[annotationUpstreams] = "db:1234"
				return pod
			},
			"-upstream=db:1234",
			"",
		},
	}

	for _, tt := range cases {
		t.Run(tt.Name, func(t *testing.T) {
			require := require.New(t)

			var h Handler
			container := h.containerSidecar(tt.Pod(minimal()))
			actual := strings.Join(container.Command, " ")
			require.Contains(actual, tt.Cmd)
			if tt.CmdNot != "" {
				require.NotContains(actual, tt.CmdNot)
			}
		})
	}
}

// encodeRaw is a helper to encode some data into a RawExtension.
func encodeRaw(t *testing.T, input interface{}) runtime.RawExtension {
	data, err := json.Marshal(input)
	require.NoError(t, err)
	return runtime.RawExtension{Raw: data}
}
