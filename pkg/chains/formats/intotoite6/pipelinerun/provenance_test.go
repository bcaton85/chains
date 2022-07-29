/*
Copyright 2020 The Tekton Authors
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pipelinerun

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	slsa "github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/v0.2"
	"github.com/tektoncd/chains/pkg/chains/formats/intotoite6/util"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	fakepipelineclient "github.com/tektoncd/pipeline/pkg/client/injection/client/fake"
	logtesting "knative.dev/pkg/logging/testing"
	rtesting "knative.dev/pkg/reconciler/testing"
)

var pr *v1beta1.PipelineRun
var e1BuildStart = time.Unix(1617011400, 0)
var e1BuildFinished = time.Unix(1617011415, 0)

// Load file once in the beginning
func init() {
	var err error
	pr, err = util.PipelinerunFromFile("../testdata/pipelinerun1.json")
	if err != nil {
		panic(err)
	}
}

func TestInvocation(t *testing.T) {
	expected := slsa.ProvenanceInvocation{
		Parameters: map[string]v1beta1.ArrayOrString{
			"IMAGE": {Type: "string", StringVal: "test.io/test/image"},
		},
	}
	got := invocation(pr, logtesting.TestLogger(t))
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Errorf("invocation(): -want +got: %s", diff)
	}
}

func TestBuildConfig(t *testing.T) {
	expected := BuildConfig{
		Tasks: []TaskAttestation{
			{
				Name:  "git-clone",
				After: nil,
				Ref: v1beta1.TaskRef{
					Name: "git-clone",
					Kind: "ClusterTask",
				},
				StartedOn:  e1BuildStart,
				FinishedOn: e1BuildFinished,
				Status:     "Succeeded",
				Steps: []util.StepAttestation{
					{
						EntryPoint: "git clone",
						Arguments:  []string(nil),
						Environment: map[string]interface{}{
							"container": "clone",
							"image":     "test.io/test/clone-image",
						},
						Annotations: nil,
					},
				},
				Invocation: slsa.ProvenanceInvocation{
					ConfigSource: slsa.ConfigSource{},
					Parameters: map[string]v1beta1.ArrayOrString{
						"revision": {Type: "string", StringVal: ""},
						"url":      {Type: "string", StringVal: "https://git.test.com"},
					},
				},
				Results: []v1beta1.TaskRunResult{
					{
						Name: "commit",
						Value: v1beta1.ArrayOrString{
							Type:      v1beta1.ParamTypeString,
							StringVal: "abcd",
						},
					},
					{
						Name: "url",
						Value: v1beta1.ArrayOrString{
							Type:      v1beta1.ParamTypeString,
							StringVal: "https://git.test.com",
						},
					},
				},
			},
			{
				Name:  "build",
				After: []string{"git-clone"},
				Ref: v1beta1.TaskRef{
					Name: "build",
					Kind: "ClusterTask",
				},
				StartedOn:  e1BuildStart,
				FinishedOn: e1BuildFinished,
				Status:     "Succeeded",
				Steps: []util.StepAttestation{
					{
						EntryPoint: "buildah build",
						Arguments:  []string(nil),
						Environment: map[string]interface{}{
							"image":     "test.io/test/build-image",
							"container": "build",
						},
						Annotations: nil,
					},
				},
				Invocation: slsa.ProvenanceInvocation{
					ConfigSource: slsa.ConfigSource{},
					Parameters: map[string]v1beta1.ArrayOrString{
						"CHAINS-GIT_COMMIT": {Type: "string", StringVal: "$(tasks.git-clone.results.commit)"},
						"CHAINS-GIT_URL":    {Type: "string", StringVal: "$(tasks.git-clone.results.url)"},
					},
				},
				Results: []v1beta1.TaskRunResult{
					{
						Name: "IMAGE_DIGEST",
						Value: v1beta1.ArrayOrString{
							Type:      v1beta1.ParamTypeString,
							StringVal: "sha256:827521c857fdcd4374f4da5442fbae2edb01e7fbae285c3ec15673d4c1daecb7",
						},
					},
					{
						Name: "IMAGE_URL",
						Value: v1beta1.ArrayOrString{
							Type:      v1beta1.ParamTypeString,
							StringVal: "test.io/test/image\n",
						},
					},
				},
			},
		},
	}
	ctx, _ := rtesting.SetupFakeContext(t)
	ps := fakepipelineclient.Get(ctx)
	got, err := buildConfig(ctx, ps, pr, logtesting.TestLogger(t))
	if err != nil {
		t.Errorf("error generating buildConfig: %s", err)
	}
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Errorf("buildConfig(): -want +got: %s", diff)
	}
}

func TestMetadata(t *testing.T) {
	expected := &slsa.ProvenanceMetadata{
		BuildStartedOn:  &e1BuildStart,
		BuildFinishedOn: &e1BuildFinished,
		Completeness: slsa.ProvenanceComplete{
			Parameters:  false,
			Environment: false,
			Materials:   false,
		},
		Reproducible: false,
	}

	got := metadata(pr)
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Errorf("metadata(): -want +got: %s", diff)
	}
}

func TestMaterials(t *testing.T) {
	expected := []slsa.ProvenanceMaterial{
		{URI: "git+https://git.test.com.git", Digest: slsa.DigestSet{"sha1": "abcd"}},
	}
	got := materials(pr)
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Errorf("materials(): -want +got: %s", diff)
	}
}
