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

package tekton

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/tektoncd/chains/pkg/chains/objects"
	"github.com/tektoncd/chains/pkg/config"

	"github.com/tektoncd/chains/pkg/patch"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	"github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	"go.uber.org/zap"
)

const (
	StorageBackendTekton      = "tekton"
	PayloadAnnotationFormat   = "chains.tekton.dev/payload-%s"
	SignatureAnnotationFormat = "chains.tekton.dev/signature-%s"
	CertAnnotationsFormat     = "chains.tekton.dev/cert-%s"
	ChainAnnotationFormat     = "chains.tekton.dev/chain-%s"
)

// Backend is a storage backend that stores signed payloads in the TaskRun metadata as an annotation.
// It is stored as base64 encoded JSON.
type Backend struct {
	pipelienclientset versioned.Interface
	logger            *zap.SugaredLogger
}

// NewStorageBackend returns a new Tekton StorageBackend that stores signatures on a TaskRun
func NewStorageBackend(ps versioned.Interface, logger *zap.SugaredLogger) *Backend {
	return &Backend{
		pipelienclientset: ps,
		logger:            logger,
	}
}

// StorePayload implements the Payloader interface.
func (b *Backend) StorePayload(ctx context.Context, clientSet versioned.Interface, obj objects.TektonObject, rawPayload []byte, signature string, opts config.StorageOpts) error {
	b.logger.Infof("Storing payload on %s/%s/%s", obj.GetKind(), obj.GetNamespace(), obj.GetName())

	// Use patch instead of update to prevent race conditions.
	patchBytes, err := patch.GetAnnotationsPatch(map[string]string{
		// Base64 encode both the signature and the payload
		fmt.Sprintf(PayloadAnnotationFormat, opts.Key):   base64.StdEncoding.EncodeToString(rawPayload),
		fmt.Sprintf(SignatureAnnotationFormat, opts.Key): base64.StdEncoding.EncodeToString([]byte(signature)),
		fmt.Sprintf(CertAnnotationsFormat, opts.Key):     base64.StdEncoding.EncodeToString([]byte(opts.Cert)),
		fmt.Sprintf(ChainAnnotationFormat, opts.Key):     base64.StdEncoding.EncodeToString([]byte(opts.Chain)),
	})
	if err != nil {
		return err
	}
	// if _, err := b.pipelienclientset.TektonV1beta1().TaskRuns(tr.Namespace).Patch(
	// 	return err
	patchErr := obj.Patch(ctx, clientSet, patchBytes)
	if patchErr != nil {
		return patchErr
	}
	return nil
}

func (b *Backend) Type() string {
	return StorageBackendTekton
}

// retrieveAnnotationValue retrieve the value of an annotation and base64 decode it if needed.
func (b *Backend) retrieveAnnotationValue(ctx context.Context, clientSet versioned.Interface, obj objects.TektonObject, annotationKey string, decode bool) (string, error) {
	// Retrieve the TaskRun.
	b.logger.Infof("Retrieving annotation %q on %s/%s/%s", annotationKey, obj.GetKind(), obj.GetNamespace(), obj.GetName())

	var annotationValue string
	annotations, err := obj.GetLatestAnnotations(ctx, clientSet)
	if err != nil {
		return "", fmt.Errorf("error retrieving the annotation value for the key %q: %s", annotationKey, err)
	}
	val, ok := annotations[annotationKey]

	// Ensure it exists.
	if ok {
		// Decode it if needed.
		if decode {
			decodedAnnotation, err := base64.StdEncoding.DecodeString(val)
			if err != nil {
				return "", fmt.Errorf("error decoding the annotation value for the key %q: %s", annotationKey, err)
			}
			annotationValue = string(decodedAnnotation)
		} else {
			annotationValue = val
		}
	}

	return annotationValue, nil
}

// RetrieveSignature retrieve the signature stored in the taskrun.
func (b *Backend) RetrieveSignatures(ctx context.Context, clientSet versioned.Interface, obj objects.TektonObject, opts config.StorageOpts) (map[string][]string, error) {
	b.logger.Infof("Retrieving signature on %s/%s/%s", obj.GetKind(), obj.GetNamespace(), obj.GetName())
	signatureAnnotation := sigName(opts)
	signature, err := b.retrieveAnnotationValue(ctx, clientSet, obj, signatureAnnotation, true)
	if err != nil {
		return nil, err
	}

	// Check if we have a pipelinerun object, if so just return the signature
	if _, ok := obj.GetObject().(*v1beta1.PipelineRun); ok {
		return map[string][]string{
			signatureAnnotation: {signature},
		}, nil
	}

	// We must have a taskrun object, so check for the IMAGE_URL param before adding signature
	if _, ok := obj.GetObject().(*v1beta1.TaskRun); !ok {
		return nil, errors.New("unrecognized object type for retrieving signatures")
	}

	m := make(map[string][]string)
	m[signatureAnnotation] = []string{signature}
	return m, nil
}

// RetrievePayload retrieve the payload stored in the taskrun.
func (b *Backend) RetrievePayloads(ctx context.Context, clientSet versioned.Interface, obj objects.TektonObject, opts config.StorageOpts) (map[string]string, error) {
	b.logger.Infof("Retrieving payload on %s/%s/%s", obj.GetKind(), obj.GetNamespace(), obj.GetName())
	payloadAnnotation := payloadName(opts)
	payload, err := b.retrieveAnnotationValue(ctx, clientSet, obj, payloadAnnotation, true)
	if err != nil {
		return nil, err
	}

	// Check if we have a pipelinerun object, if so just return the signature
	if _, ok := obj.GetObject().(*v1beta1.PipelineRun); ok {
		return map[string]string{
			payloadAnnotation: payload,
		}, nil
	}

	// We must have a taskrun object, so check for the IMAGE_URL param before adding signature
	if _, ok := obj.GetObject().(*v1beta1.TaskRun); !ok {
		return nil, errors.New("unrecognized object type for retrieving payload")
	}

	m := make(map[string]string)
	m[payloadAnnotation] = payload
	return m, nil
}

func sigName(opts config.StorageOpts) string {
	return fmt.Sprintf(SignatureAnnotationFormat, opts.Key)
}

func payloadName(opts config.StorageOpts) string {
	return fmt.Sprintf(PayloadAnnotationFormat, opts.Key)
}
