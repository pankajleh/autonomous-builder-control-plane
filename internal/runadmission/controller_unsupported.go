//go:build !linux

package runadmission

import (
	"context"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

func NewController(Config) (*Controller, error) { return nil, serviceapi.ErrAdmissionUnavailable }

func (c *Controller) admitRun(context.Context, serviceapi.Principal, serviceapi.RunAdmissionRequestV1) (serviceapi.RunAdmissionResponseV1, error) {
	return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrAdmissionUnavailable
}

func (c *Controller) admitDevelopmentRun(context.Context, serviceapi.Principal, serviceapi.DevelopmentRunAdmissionRequestV1) (serviceapi.RunAdmissionResponseV1, error) {
	return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrAdmissionUnavailable
}

func closeFD(int) error { return nil }
