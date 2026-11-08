package api

import (
	"context"
	"errors"
	"strings"
	"github.com/eutelic/runnerhub/internal/core"
	"github.com/eutelic/runnerhub/internal/githubrunner"
)

func(s *Service)inspectSecurity(ctx context.Context,p core.RunnerPool)(githubrunner.PoolSecurityPosture,error){if s.Catalog==nil{return githubrunner.PoolSecurityPosture{},errors.New("github catalog is not configured")};return githubrunner.InspectPoolSecurity(ctx,s.Catalog,p)}
func(s *Service)requireSafeSecurity(ctx context.Context,p core.RunnerPool)(githubrunner.PoolSecurityPosture,error){posture,err:=s.inspectSecurity(ctx,p);if err!=nil{return posture,err};if !posture.Safe{return posture,errors.New(strings.Join(posture.Reasons,"; "))};return posture,nil}
