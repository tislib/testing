// Code generated by apbr generate. DO NOT EDIT.
// versions:
// 	apbr generate v1.2

//go:build !codeanalysis

package model

import "github.com/google/uuid"

type TestExecution struct {
	Id       *uuid.UUID           `json:"id,omitempty"`
	Stored   bool                 `json:"stored,omitempty"`
	Name     string               `json:"name,omitempty"`
	TestCase *TestCase            `json:"testCase,omitempty"`
	Result   *TestExecutionResult `json:"result,omitempty"`
	Logs     *string              `json:"logs,omitempty"`
	Version  int32                `json:"version,omitempty"`
}

func (s *TestExecution) GetId() *uuid.UUID {
	return s.Id
}
func (s *TestExecution) GetStored() bool {
	return s.Stored
}
func (s *TestExecution) GetName() string {
	return s.Name
}
func (s *TestExecution) GetTestCase() *TestCase {
	return s.TestCase
}
func (s *TestExecution) GetResult() *TestExecutionResult {
	return s.Result
}
func (s *TestExecution) GetLogs() *string {
	return s.Logs
}
func (s *TestExecution) GetVersion() int32 {
	return s.Version
}

type TestExecutionResult string

const (
	TestExecutionResult_SUCCESS TestExecutionResult = "SUCCESS"
	TestExecutionResult_FAILURE TestExecutionResult = "FAILURE"
)
