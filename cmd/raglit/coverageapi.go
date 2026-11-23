package main

import (
	"context"

	"github.com/iodesystems/raglit"
	"github.com/danielgtaylor/huma/v2"
)

// coverageOut is the response for GET /api/coverage.
type coverageOut struct {
	Body raglit.CorpusCoverage `json:"coverage"`
}

func coverageOp(reg *raglit.Registry) func(context.Context, *indexOnlyIn) (*coverageOut, error) {
	return func(ctx context.Context, in *indexOnlyIn) (*coverageOut, error) {
		st, err := readIdx(ctx, reg, in.Index)
		if err != nil {
			return nil, err
		}
		c, err := st.CoverageFor("")
		if err != nil {
			return nil, huma.Error500InternalServerError("coverage", err)
		}
		return &coverageOut{Body: c}, nil
	}
}
