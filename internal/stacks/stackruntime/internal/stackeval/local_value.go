// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: BUSL-1.1

package stackeval

import (
	"context"
	"fmt"

	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"

	"github.com/hashicorp/terraform/internal/collections"
	"github.com/hashicorp/terraform/internal/promising"
	"github.com/hashicorp/terraform/internal/stacks/stackaddrs"
	"github.com/hashicorp/terraform/internal/stacks/stackconfig"
	"github.com/hashicorp/terraform/internal/stacks/stackplan"
	"github.com/hashicorp/terraform/internal/stacks/stackstate"
	"github.com/hashicorp/terraform/internal/tfdiags"
)

// LocalValue represents an input variable belonging to a [Stack].
type LocalValue struct {
	addr stackaddrs.AbsLocalValue

	main *Main

	value perEvalPhase[promising.Once[withDiagnostics[cty.Value]]]
}

var _ Referenceable = (*LocalValue)(nil)
var _ Plannable = (*LocalValue)(nil)

func newLocalValue(main *Main, addr stackaddrs.AbsLocalValue) *LocalValue {
	return &LocalValue{
		addr: addr,
		main: main,
	}
}

func (v *LocalValue) Addr() stackaddrs.AbsLocalValue {
	return v.addr
}

func (v *LocalValue) Config(ctx context.Context) *LocalValueConfig {
	configAddr := stackaddrs.ConfigForAbs(v.Addr())
	stackCfg := v.main.StackConfig(ctx, configAddr.Stack)
	return stackCfg.LocalValue(ctx, configAddr.Item)
}

func (v *LocalValue) Declaration(ctx context.Context) *stackconfig.LocalValue {
	return v.Config(ctx).Declaration()
}

func (v *LocalValue) Stack(ctx context.Context, phase EvalPhase) *Stack {
	return v.main.Stack(ctx, v.Addr().Stack, phase)
}

func (v *LocalValue) Value(ctx context.Context, phase EvalPhase) cty.Value {
	val, _ := v.CheckValue(ctx, phase)
	return val
}

// ExprReferenceValue implements Referenceable.
func (v *LocalValue) ExprReferenceValue(ctx context.Context, phase EvalPhase) cty.Value {
	return v.Value(ctx, phase)
}

func (v *LocalValue) checkValid(ctx context.Context, phase EvalPhase) tfdiags.Diagnostics {
	var diags tfdiags.Diagnostics

	_, moreDiags := v.CheckValue(ctx, phase)
	diags = diags.Append(moreDiags)

	return diags
}

func (v *LocalValue) CheckValue(ctx context.Context, phase EvalPhase) (cty.Value, tfdiags.Diagnostics) {
	return withCtyDynamicValPlaceholder(doOnceWithDiags(
		ctx, v.value.For(phase), v.main,
		func(ctx context.Context) (cty.Value, tfdiags.Diagnostics) {
			var diags tfdiags.Diagnostics

			decl := v.Declaration(ctx)
			stack := v.Stack(ctx, phase)

			if stack == nil {
				// TODO(mutahhir): Needs review by someone who knows the codebase better.
				// In OutputValue, this is returning an unknown value with type, but
				// locals don't have a type, so Dynamic makes some sense here, but
				// I don't know how it would affect other things
				return cty.DynamicVal, diags
			}

			result, moreDiags := EvalExprAndEvalContext(ctx, decl.Value, phase, stack)
			diags = diags.Append(moreDiags)
			if moreDiags.HasErrors() {
				return cty.DynamicVal, diags
			}

			var err error
			result.Value, err = convert.Convert(result.Value, cty.DynamicPseudoType)

			if err != nil {
				diags = diags.Append(result.Diagnostic(
					tfdiags.Error,
					"Invalid local value",
					fmt.Sprintf("Unsuitable value for local %q: %s.", v.Addr().Item.Name, tfdiags.FormatError(err)),
				))
				return cty.DynamicVal, diags
			}

			return result.Value, diags
		},
	))
}

// PlanChanges implements Plannable as a plan-time validation of the variable's
// declaration and of the caller's definition of the variable.
func (v *LocalValue) PlanChanges(ctx context.Context) ([]stackplan.PlannedChange, tfdiags.Diagnostics) {
	return nil, v.checkValid(ctx, PlanPhase)
}

// References implements Referrer
func (v *LocalValue) References(ctx context.Context) []stackaddrs.AbsReference {
	cfg := v.Declaration(ctx)
	var ret []stackaddrs.Reference
	ret = append(ret, ReferencesInExpr(ctx, cfg.Value)...)
	return makeReferencesAbsolute(ret, v.Addr().Stack)
}

// RequiredComponents implements Applyable
func (v *LocalValue) RequiredComponents(ctx context.Context) collections.Set[stackaddrs.AbsComponent] {
	return v.main.requiredComponentsForReferrer(ctx, v, PlanPhase)
}

// CheckApply implements Applyable.
func (v *LocalValue) CheckApply(ctx context.Context) ([]stackstate.AppliedChange, tfdiags.Diagnostics) {
	return nil, v.checkValid(ctx, ApplyPhase)
}

func (v *LocalValue) tracingName() string {
	return v.Addr().String()
}