package pipelinerun

import (
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

func GenerateAttestation(builderID string, pro *objects.PipelineRunObject, logger *zap.SugaredLogger) (interface{}, error) {
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
			BuildType:   util.TektonPipelineRunID,
			Invocation:  invocation(pro, logger),
			BuildConfig: buildConfig(pro, logger),
			Metadata:    metadata(pro),
			Materials:   materials(pro),
		},
	}
	return att, nil
}

func invocation(pro *objects.PipelineRunObject, logger *zap.SugaredLogger) slsa.ProvenanceInvocation {
	var paramSpecs []v1beta1.ParamSpec
	if ps := pro.Status.PipelineSpec; ps != nil {
		paramSpecs = ps.Params
	}
	return util.AttestInvocation(pro.Spec.Params, paramSpecs, logger)
}

func buildConfig(pro *objects.PipelineRunObject, logger *zap.SugaredLogger) BuildConfig {
	tasks := []TaskAttestation{}

	pSpec := pro.Status.PipelineSpec
	if pSpec == nil {
		return BuildConfig{}
	}
	pipelineTasks := append(pSpec.Tasks, pSpec.Finally...)

	var last string
	for i, t := range pipelineTasks {
		tr := pro.GetTaskRunFromTask(t.Name)

		// Ignore Tasks that did not execute during the PipelineRun.
		if tr == nil || tr.Status.CompletionTime == nil {
			logger.Infof("taskrun status not found for task %s", t.Name)
			continue
		}
		steps := []util.StepAttestation{}
		for i, step := range tr.Status.Steps {
			stepState := tr.Status.TaskSpec.Steps[i]
			steps = append(steps, util.AttestStep(&stepState, &step))
		}
		after := t.RunAfter

		// Establish task order by retrieving all task's referenced
		// in the "when" and "params" fields
		refs := v1beta1.PipelineTaskResultRefs(&t)
		for _, ref := range refs {

			// Ensure task doesn't already exist in after
			found := false
			for _, at := range after {
				if at == ref.PipelineTask {
					found = true
				}
			}
			if !found {
				after = append(after, ref.PipelineTask)
			}
		}

		// tr is a finally task without an explicit runAfter value. It must have executed
		// after the last non-finally task, if any non-finally tasks were executed.
		if len(after) == 0 && i >= len(pSpec.Tasks) && last != "" {
			after = append(after, last)
		}
		params := t.Params
		var paramSpecs []v1beta1.ParamSpec
		if tr.Status.TaskSpec != nil {
			paramSpecs = tr.Status.TaskSpec.Params
		} else {
			paramSpecs = []v1beta1.ParamSpec{}
		}
		task := TaskAttestation{
			Name:       t.Name,
			After:      after,
			StartedOn:  tr.Status.StartTime.Time,
			FinishedOn: tr.Status.CompletionTime.Time,
			Status:     getStatus(tr.Status.Conditions),
			Steps:      steps,
			Invocation: util.AttestInvocation(params, paramSpecs, logger),
			Results:    tr.Status.TaskRunResults,
		}

		if t.TaskRef != nil {
			task.Ref = *t.TaskRef
		}

		tasks = append(tasks, task)
		if i < len(pSpec.Tasks) {
			last = task.Name
		}
	}
	return BuildConfig{Tasks: tasks}
}

func metadata(pro *objects.PipelineRunObject) *slsa.ProvenanceMetadata {
	m := &slsa.ProvenanceMetadata{}
	if pro.Status.StartTime != nil {
		m.BuildStartedOn = &pro.Status.StartTime.Time
	}
	if pro.Status.CompletionTime != nil {
		m.BuildFinishedOn = &pro.Status.CompletionTime.Time
	}
	for label, value := range pro.Labels {
		if label == util.ChainsReproducibleAnnotation && value == "true" {
			m.Reproducible = true
		}
	}
	return m
}

// add any Git specification to materials
func materials(pro *objects.PipelineRunObject) []slsa.ProvenanceMaterial {
	var mats []slsa.ProvenanceMaterial
	var commit, url string
	// search spec.params
	for _, p := range pro.Spec.Params {
		if p.Name == util.CommitParam {
			commit = p.Value.StringVal
			continue
		}
		if p.Name == util.UrlParam {
			url = p.Value.StringVal
		}
	}

	// search status.PipelineSpec.params
	if pro.Status.PipelineSpec != nil {
		for _, p := range pro.Status.PipelineSpec.Params {
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
	for _, r := range pro.Status.PipelineResults {
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
