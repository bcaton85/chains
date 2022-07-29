package pipelinerun

import (
	"context"
	"time"

	intoto "github.com/in-toto/in-toto-golang/in_toto"
	slsa "github.com/in-toto/in-toto-golang/in_toto/slsa_provenance/v0.2"
	"github.com/tektoncd/chains/pkg/chains/formats/intotoite6/util"
	"github.com/tektoncd/chains/pkg/chains/objects"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	versioned "github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	"github.com/tektoncd/pipeline/pkg/status"
	"go.uber.org/zap"

	corev1 "k8s.io/api/core/v1"
	"knative.dev/pkg/apis"
)

type BuildConfig struct {
	Tasks []TaskAttestation `json:"tasks"`
}

type TaskAttestation struct {
	Name       string                    `json:"name,omitempty"`
	After      []string                  `json:"after,omitempty"`
	Ref        v1beta1.TaskRef           `json:"ref,omitempty"`
	StartedOn  time.Time                 `json:"startedOn,omitempty"`
	FinishedOn time.Time                 `json:"finishedOn,omitempty"`
	Status     string                    `json:"status,omitempty"`
	Steps      []util.StepAttestation    `json:"steps,omitempty"`
	Invocation slsa.ProvenanceInvocation `json:"invocation,omitempty"`
	Results    []v1beta1.TaskRunResult   `json:"results,omitempty"`
}

func GenerateAttestation(ctx context.Context, c versioned.Interface, builderID string, pr *v1beta1.PipelineRun, logger *zap.SugaredLogger) (interface{}, error) {
	pro := objects.NewPipelineRunObject(pr)
	subjects := util.GetSubjectDigests(pro, logger)
	bc, err := buildConfig(ctx, c, pr, logger)
	if err != nil {
		return nil, err
	}

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
			BuildType:   util.TektonPipelineRunID,
			Invocation:  invocation(pr, logger),
			BuildConfig: bc,
			Metadata:    metadata(pr),
			Materials:   materials(pr),
		},
	}
	return att, nil
}

func invocation(pr *v1beta1.PipelineRun, logger *zap.SugaredLogger) slsa.ProvenanceInvocation {
	var paramSpecs []v1beta1.ParamSpec
	if ps := pr.Status.PipelineSpec; ps != nil {
		paramSpecs = ps.Params
	}
	return util.AttestInvocation(pr.Spec.Params, paramSpecs, logger)
}

func buildConfig(ctx context.Context, c versioned.Interface, pr *v1beta1.PipelineRun, logger *zap.SugaredLogger) (BuildConfig, error) {
	tasks := []TaskAttestation{}

	// status.GetFullPipelineTaskStatuses doesn't maintain order,
	// so we'll store status' separately and reference them later

	// TODO: We're only supporting TaskRun tasks for now. The second parameter
	// returns a list of Run status', which are an alpha feature. Support can
	// be added later
	trStatuses, _, err := status.GetFullPipelineTaskStatuses(ctx, c, pr.Namespace, pr)
	if err != nil {
		return BuildConfig{}, err
	}

	pSpec := pr.Status.PipelineSpec
	if pSpec == nil {
		return BuildConfig{}, nil
	}
	pipelineTasks := append(pSpec.Tasks, pSpec.Finally...)

	var last string
	for i, t := range pipelineTasks {
		var trStatus *v1beta1.PipelineRunTaskRunStatus
		for _, status := range trStatuses {
			if status.PipelineTaskName == t.Name {
				trStatus = status
				break
			}
		}

		// Ignore Tasks that did not execute during the PipelineRun.
		if trStatus == nil || trStatus.Status == nil {
			logger.Infof("status not found for taskrun %s", t.Name)
			continue
		}

		steps := []util.StepAttestation{}
		for i, step := range trStatus.Status.Steps {
			stepState := trStatus.Status.TaskSpec.Steps[i]
			steps = append(steps, util.AttestStep(&stepState, &step))
		}
		after := t.RunAfter
		// tr is a finally task without an explicit runAfter value. It must have executed
		// after the last non-finally task, if any non-finally tasks were executed.
		if len(after) == 0 && i >= len(pSpec.Tasks) && last != "" {
			after = append(after, last)
		}
		params := t.Params
		var paramSpecs []v1beta1.ParamSpec
		if trStatus.Status.TaskSpec != nil {
			paramSpecs = trStatus.Status.TaskSpec.Params
		} else {
			paramSpecs = []v1beta1.ParamSpec{}
		}
		task := TaskAttestation{
			Name:       t.Name,
			After:      after,
			StartedOn:  trStatus.Status.StartTime.Time,
			FinishedOn: trStatus.Status.CompletionTime.Time,
			Status:     getStatus(trStatus.Status.Conditions),
			Steps:      steps,
			Invocation: util.AttestInvocation(params, paramSpecs, logger),
			Results:    trStatus.Status.TaskRunResults,
		}

		if t.TaskRef != nil {
			task.Ref = *t.TaskRef
		}

		tasks = append(tasks, task)
		if i < len(pSpec.Tasks) {
			last = task.Name
		}
	}
	return BuildConfig{Tasks: tasks}, nil
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

	// search status.PipelineRunResults
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
