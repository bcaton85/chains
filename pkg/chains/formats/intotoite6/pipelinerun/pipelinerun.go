package pipelinerun

import (
	"fmt"
	"time"

	intoto "github.com/in-toto/in-toto-golang/in_toto"
	slsa "github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/v0.2"
	"github.com/tektoncd/chains/pkg/chains/formats/intotoite6/util"
	"github.com/tektoncd/chains/pkg/chains/objects"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"knative.dev/pkg/apis"
)

type BuildConfig struct {
	Tasks []Task `json:"tasks"`
}

type Task struct {
	Name       string    `json:"name,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	Status     string    `json:"status,omitempty"`
	Steps      []Step    `json:"steps,omitempty"`
}

type Step struct {
	StepState v1beta1.StepState `json:"stepState,omitempty"`
	Command   []string          `json:"command,omitempty"`
	Arguments []string          `json:"arguments,omitempty"`
	Script    string            `json:"script,omitempty"`
}

func GenerateAttestation(builderID string, pr *v1beta1.PipelineRun, logger *zap.SugaredLogger) (interface{}, error) {
	// We don't need to pass in a client/context here, as we only want access to the original object
	// Need a better way to differentiate between an abstracted original k8s object and getting the latest values
	// - Possibly pass client and context in each method call instead of adding it to the struct?
	pro := objects.NewPipelineRunObject(pr, nil, nil)
	subjects := util.GetSubjectDigests(pro, logger)

	att := intoto.ProvenanceStatement{
		StatementHeader: intoto.StatementHeader{
			Type:          intoto.StatementInTotoV01,
			PredicateType: slsa.PredicateSLSAProvenance,
			Subject:       subjects,
		},
		Predicate: slsa.ProvenancePredicate{
			Builder: slsa.ProvenanceBuilder{
				ID: builderID,
			},
			BuildType:   util.TektonID,
			Invocation:  invocation(pr),
			BuildConfig: buildConfig(pr),
			Metadata:    metadata(pr),
			Materials:   materials(pr),
		},
	}
	return att, nil
}

func invocation(pr *v1beta1.PipelineRun) slsa.ProvenanceInvocation {
	i := slsa.ProvenanceInvocation{}
	params := make(map[string]string)
	// add params
	if ts := pr.Status.PipelineSpec; ts != nil {
		for _, p := range ts.Params {
			if p.Default != nil {
				v := p.Default.StringVal
				if v == "" {
					v = fmt.Sprintf("%v", p.Default.ArrayVal)
				}
				params[p.Name] = v
			}
		}
	}
	// get parameters
	for _, p := range pr.Spec.Params {
		v := p.Value.StringVal
		if v == "" {
			v = fmt.Sprintf("%v", p.Value.ArrayVal)
		}
		params[p.Name] = v
	}
	i.Parameters = params
	return i
}

func buildConfig(pr *v1beta1.PipelineRun) BuildConfig {
	tasks := []Task{}

	// pipelineRun.status.taskRuns doesn't maintain order,
	// so we'll store here and use the order from pipelineRun.status.pipelineSpec.tasks
	taskRuns := make(map[string]*v1beta1.PipelineRunTaskRunStatus)
	for _, tr := range pr.Status.TaskRuns {
		taskRuns[tr.PipelineTaskName] = tr
	}

	for _, tr := range pr.Status.PipelineSpec.Tasks {
		trStatus := taskRuns[tr.Name]
		steps := []Step{}
		for i, step := range trStatus.Status.Steps {
			steps = append(steps, Step{
				StepState: step,
				Command:   trStatus.Status.TaskSpec.Steps[i].Command,
				Arguments: trStatus.Status.TaskSpec.Steps[i].Args,
				Script:    trStatus.Status.TaskSpec.Steps[i].Script,
			})
		}
		task := Task{
			Name:       trStatus.PipelineTaskName,
			StartedAt:  trStatus.Status.StartTime.Time,
			FinishedAt: trStatus.Status.CompletionTime.Time,
			Status:     getStatus(trStatus.Status.Conditions),
			Steps:      steps,
		}
		tasks = append(tasks, task)
	}
	return BuildConfig{Tasks: tasks}
}

func metadata(pr *v1beta1.PipelineRun) *slsa.ProvenanceMetadata {
	m := &slsa.ProvenanceMetadata{}
	if pr.Status.StartTime != nil {
		m.BuildStartedOn = &pr.Status.StartTime.Time
	}
	if pr.Status.CompletionTime != nil {
		m.BuildFinishedOn = &pr.Status.CompletionTime.Time
	}
	for label, value := range pr.Labels {
		if label == util.ChainsReproducibleAnnotation && value == "true" {
			m.Reproducible = true
		}
	}
	return m
}

// add any Git specification to materials
func materials(pr *v1beta1.PipelineRun) []slsa.ProvenanceMaterial {
	var mats []slsa.ProvenanceMaterial
	var commit, url string
	// search spec.params
	for _, p := range pr.Spec.Params {
		if p.Name == util.CommitParam {
			commit = p.Value.StringVal
			continue
		}
		if p.Name == util.UrlParam {
			url = p.Value.StringVal
		}
	}

	// search status.PipelineSpec.params
	if pr.Status.PipelineSpec != nil {
		for _, p := range pr.Status.PipelineSpec.Params {
			if p.Default == nil {
				continue
			}
			if p.Name == util.CommitParam {
				commit = p.Default.StringVal
				continue
			}
			if p.Name == util.UrlParam {
				url = p.Default.StringVal
			}
		}
	}

	// search status.TaskRunResults
	for _, r := range pr.Status.PipelineResults {
		if r.Name == util.CommitParam {
			commit = r.Value
		}
		if r.Name == util.UrlParam {
			url = r.Value
		}
	}
	url = util.SpdxGit(url, "")
	mats = append(mats, slsa.ProvenanceMaterial{
		URI:    url,
		Digest: map[string]string{"sha1": commit},
	})
	return mats
}

// Following tkn cli's behavior
// https://github.com/tektoncd/cli/blob/6afbb0f0dbc7186898568f0d4a0436b8b2994d99/pkg/formatted/k8s.go#L55
func getStatus(conditions []apis.Condition) string {
	var status string
	if len(conditions) > 0 {
		switch conditions[0].Status {
		case corev1.ConditionFalse:
			status = "Failed"
		case corev1.ConditionTrue:
			status = "Succeeded"
		case corev1.ConditionUnknown:
			status = "Running" // Should never happen
		}
	}
	return status
}
