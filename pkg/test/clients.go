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

package test

import (
	"context"
	"fmt"

	"github.com/tektoncd/chains/pkg/chains/objects"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	pipelineclientset "github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func CreateTektonObject(ctx context.Context, ps pipelineclientset.Interface, obj objects.TektonObject) error {
	switch o := obj.GetObject().(type) {
	case *v1beta1.PipelineRun:
		if _, err := ps.TektonV1beta1().PipelineRuns(obj.GetNamespace()).Create(ctx, o, metav1.CreateOptions{}); err != nil {
			return err
		}
	case *v1beta1.TaskRun:
		if _, err := ps.TektonV1beta1().TaskRuns(obj.GetNamespace()).Create(ctx, o, metav1.CreateOptions{}); err != nil {
			return err
		}
	}
	return nil
}

// Passing in TektonObject since it encapsulates namespace, name, and type.
func GetTektonObject(ctx context.Context, ps pipelineclientset.Interface, obj objects.TektonObject) (objects.TektonObject, error) {
	switch obj.GetObject().(type) {
	case *v1beta1.PipelineRun:
		return GetPipelineRun(ctx, ps, obj.GetNamespace(), obj.GetName())
	case *v1beta1.TaskRun:
		return GetTaskRun(ctx, ps, obj.GetNamespace(), obj.GetName())
	}
	return nil, fmt.Errorf("unknown object type %T", obj.GetObject())
}

func GetPipelineRun(ctx context.Context, ps pipelineclientset.Interface, namespace, name string) (objects.TektonObject, error) {
	pr, err := ps.TektonV1beta1().PipelineRuns(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return objects.NewPipelineRunObject(pr), nil
}

func GetTaskRun(ctx context.Context, ps pipelineclientset.Interface, namespace, name string) (objects.TektonObject, error) {
	tr, err := ps.TektonV1beta1().TaskRuns(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return objects.NewTaskRunObject(tr), nil
}
