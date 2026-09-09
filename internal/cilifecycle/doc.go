// Package cilifecycle records bounded, neutral GitHub CI observations.
//
// A stable collection means only that two complete semantic sweeps matched
// while the governed head ref remained unchanged. It does not mean that CI
// passed and it does not authorize a merge.
package cilifecycle
