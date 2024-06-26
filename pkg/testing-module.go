package pkg

import (
	"context"
	"fmt"
	"github.com/apibrew/apibrew/pkg/api"
	"github.com/apibrew/apibrew/pkg/errors"
	"github.com/apibrew/apibrew/pkg/formats/unstructured"
	"github.com/apibrew/apibrew/pkg/formats/unstructured/ops"
	"github.com/apibrew/apibrew/pkg/model"
	"github.com/apibrew/apibrew/pkg/resources"
	"github.com/apibrew/apibrew/pkg/service"
	backend_event_handler "github.com/apibrew/apibrew/pkg/service/backend-event-handler"
	"github.com/apibrew/apibrew/pkg/util"
	model2 "github.com/apibrew/template/pkg/model"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/types/known/structpb"
	"time"

	"reflect"
	"strings"
)

type module struct {
	container           service.Container
	backendEventHandler backend_event_handler.BackendEventHandler
	apiInterface        api.Interface
}

func (m module) Init() {
	m.ensureNamespace()
	m.ensureResources()
	m.initTestExecutionListeners()
}

func (m module) ensureNamespace() {
	_, err := m.container.GetRecordService().Apply(util.SystemContext, service.RecordUpdateParams{
		Namespace: resources.NamespaceResource.Namespace,
		Resource:  resources.NamespaceResource.Name,
		Records: []*model.Record{
			{
				Properties: map[string]*structpb.Value{
					"name": structpb.NewStringValue("testing"),
				},
			},
		},
	})

	if err != nil {
		log.Panic(err)
	}
}

func (m module) ensureResources() {
	var list = []*model.Resource{model2.TestCaseResource, model2.TestExecutionResource}

	for _, resource := range list {
		existingResource, err := m.container.GetResourceService().GetResourceByName(util.SystemContext, resource.Namespace, resource.Name)

		if err == nil {
			resource.Id = existingResource.Id
			err = m.container.GetResourceService().Update(util.SystemContext, resource, true, true)

			if err != nil {
				log.Panic(err)
			}
		} else if err.Is(errors.ResourceNotFoundError) {
			_, err = m.container.GetResourceService().Create(util.SystemContext, resource, true, true)

			if err != nil {
				log.Panic(err)
			}
		} else if err != nil {
			log.Panic(err)
		}
	}
}

func (m module) initTestExecutionListeners() {
	m.backendEventHandler.RegisterHandler(backend_event_handler.Handler{
		Id:   "test-execution-listener",
		Name: "test-execution-listener",
		Fn:   m.handleTestExecution,
		Selector: &model.EventSelector{
			Actions: []model.Event_Action{
				model.Event_CREATE,
			},
			Namespaces: []string{model2.TestExecutionResource.Namespace},
			Resources:  []string{model2.TestExecutionResource.Name},
		},
		Order:    90,
		Sync:     true,
		Responds: true,
		Internal: true,
	})

	m.backendEventHandler.RegisterHandler(backend_event_handler.Handler{
		Id:   "test-case-listener",
		Name: "test-case-listener",
		Fn:   m.handleTestCase,
		Selector: &model.EventSelector{
			Actions: []model.Event_Action{
				model.Event_CREATE,
				model.Event_UPDATE,
			},
			Namespaces: []string{model2.TestCaseResource.Namespace},
			Resources:  []string{model2.TestCaseResource.Name},
		},
		Order:    90,
		Sync:     true,
		Responds: true,
		Internal: true,
	})
}

func (m module) handleTestCase(ctx context.Context, event *model.Event) (*model.Event, errors.ServiceError) {
	for idx, record := range event.Records {
		testCase := model2.TestCaseMapperInstance.FromRecord(record)

		if testCase.AutoRun {
			testCase.AutoRun = false
			record = model2.TestCaseMapperInstance.ToRecord(testCase)
			event.Records[idx] = record

			execution := &model2.TestExecution{
				TestCase: testCase,
			}

			var variableMap = &map[string]interface{}{}

			err := m.executeTestCase(ctx, execution, variableMap)

			if err != nil {
				return nil, err
			}
		}
	}

	return event, nil
}

func (m module) handleTestExecution(ctx context.Context, event *model.Event) (*model.Event, errors.ServiceError) {
	var recordsToDelete []string

	defer func() {
		go func() {
			time.Sleep(1 * time.Second)
			if len(recordsToDelete) > 0 {
				err := m.container.GetRecordService().Delete(util.SystemContext, service.RecordDeleteParams{
					Namespace: model2.TestExecutionResource.Namespace,
					Resource:  model2.TestExecutionResource.Name,
					Ids:       recordsToDelete,
				})

				if err != nil {
					log.Warn(err)
				}
			}
		}()
	}()

	for _, record := range event.Records {
		err, store := m.executeTest(ctx, record)

		if err != nil {
			return nil, err
		}

		if !store {
			recordsToDelete = append(recordsToDelete, util.GetRecordId(record))
		}
	}

	return event, nil
}

func (m module) executeTest(ctx context.Context, record *model.Record) (errors.ServiceError, bool) {
	// locating records
	// locating test execution

	err := m.container.GetRecordService().ResolveReferences(ctx, model2.TestExecutionResource, []*model.Record{record}, []string{"$.testCase", "$.testCase.testSuite"})

	if err != nil {
		return err, false
	}

	testExecution := model2.TestExecutionMapperInstance.FromRecord(record)
	testExecution.Logs = util.Pointer("")

	var variableMap = &map[string]interface{}{}

	// executing test
	return m.executeTestCase(ctx, testExecution, variableMap), testExecution.Stored
}

func (m module) executeTestCase(ctx context.Context, execution *model2.TestExecution, variableMap *map[string]interface{}) errors.ServiceError {
	m.log(execution, "Executing test case '%s'", execution.TestCase.Name)
	// executing steps
	m.log(execution, "Executing test case steps begin '%s'", execution.TestCase.Name)

	for _, step := range execution.TestCase.Steps {
		// check if there are error from previous step

		if (*variableMap)["error"] != nil {
			m.log(execution, "Executing test case step skipped '%s' => %s", util.DePointer(step.Name, ""), (*variableMap)["error"].(error).Error())
			return nil
		}

		m.log(execution, "Executing test case step '%s'", util.DePointer(step.Name, ""))
		err := m.executeStep(ctx, execution, step, variableMap)

		if err != nil {
			(*variableMap)["error"] = err
		}
		m.log(execution, "Executing test case step done '%s'", util.DePointer(step.Name, ""))
	}
	m.log(execution, "Executing test case steps done '%s'", execution.TestCase.Name)

	m.log(execution, "Executing test case assertions begin '%s'", execution.TestCase.Name)
	for _, assertion := range execution.TestCase.Assertions {
		m.log(execution, "Executing test case assertion '%s'", util.DePointer(assertion.Name, ""))
		err := m.executeAssertion(ctx, execution, assertion, variableMap)

		if err != nil {
			m.log(execution, "Executing test case assertion failed '%s' => %s", util.DePointer(assertion.Name, ""), err.Error())
			return err
		}
		m.log(execution, "Executing test case assertion done '%s'", util.DePointer(assertion.Name, ""))
	}

	if (*variableMap)["error"] != nil {
		m.log(execution, "Executing test case step failed(last step) %s", (*variableMap)["error"].(error).Error())
		return errors.RecordValidationError.WithMessage((*variableMap)["error"].(error).Error())
	}

	m.log(execution, "Executing test case assertions done '%s'", execution.TestCase.Name)
	// executing assertions done
	m.log(execution, "Test case %s executed", execution.TestCase.Name)

	return nil
}

func (m module) executeStep(ctx context.Context, execution *model2.TestExecution, step model2.TestCaseTestCaseStep, variableMap *map[string]interface{}) errors.ServiceError {
	switch step.Operation {
	case model2.TestCaseOperation_CREATE:
		return m.executeCreate(ctx, execution, step, variableMap)
	case model2.TestCaseOperation_UPDATE:
		return m.executeUpdate(ctx, execution, step, variableMap)
	case model2.TestCaseOperation_DELETE:
		return m.executeDelete(ctx, execution, step, variableMap)
	case model2.TestCaseOperation_GET:
		return m.executeGet(ctx, execution, step, variableMap)
	case model2.TestCaseOperation_LIST:
		return m.executeList(ctx, execution, step, variableMap)
	case model2.TestCaseOperation_APPLY:
		return m.executeApply(ctx, execution, step, variableMap)
	case model2.TestCaseOperation_NANO:
		return m.executeNano(ctx, execution, step, variableMap)
	}

	return nil
}

func (m module) executeAssertion(ctx context.Context, execution *model2.TestExecution, step model2.TestCaseTestCaseAssertion, variableMap *map[string]interface{}) errors.ServiceError {

	left, err := m.resolveValue(step.Left, variableMap)

	if err != nil {
		return err
	}

	right, err := m.resolveValue(step.Right, variableMap)

	if err != nil {
		return err
	}

	switch step.AssertionType {
	case model2.TestCaseAssertionType_EQUAL:
		if fmt.Sprintf("%v", left) != fmt.Sprintf("%v", right) {
			return errors.RecordValidationError.WithMessage(fmt.Sprintf("Assertion failed: %v != %v", left, right))
		}
	case model2.TestCaseAssertionType_NOTEQUAL:
		if reflect.DeepEqual(left, right) {
			return errors.RecordValidationError.WithMessage(fmt.Sprintf("Assertion failed: %v == %v", left, right))
		}
	case model2.TestCaseAssertionType_EXPECTERROR:
		if serr, ok := (*variableMap)["error"].(errors.ServiceError); ok {
			if step.ErrorCode != nil && *step.ErrorCode != serr.Code().String() {
				return errors.RecordValidationError.WithMessage(fmt.Sprintf("Assertion failed for error code: %v != %v", util.DePointer(step.ErrorCode, ""), serr.Code().String()))
			}

			var actualMessage = serr.GetFullMessage()
			if step.ErrorMessage != nil && *step.ErrorMessage != actualMessage {
				return errors.RecordValidationError.WithMessage(fmt.Sprintf("Assertion failed for error code: %s != %s", util.DePointer(step.ErrorMessage, ""), serr.GetFullMessage()))
			}

			delete(*variableMap, "error")
		}
	}

	return nil
}

func (m module) resolveValue(value interface{}, variableMap *map[string]interface{}) (interface{}, errors.ServiceError) {
	if ptr, ok := value.(*interface{}); ok {
		if ptr == nil {
			return nil, nil
		}
		value = *ptr
	}
	if ptr, ok := value.(*string); ok {
		if ptr == nil {
			return nil, nil
		}
		value = *ptr
	}
	if uns, ok := value.(*unstructured.Unstructured); ok {
		if uns == nil {
			return nil, nil
		}
		value = *uns
	}

	if uns, ok := value.(unstructured.Unstructured); ok {
		processed, err := ops.WalkUnstructured(uns, func(value interface{}) (interface{}, error) {
			if str, ok := value.(string); ok {
				return m.resolveValue(str, variableMap)
			}

			return value, nil
		})

		if err != nil {
			return nil, errors.RecordValidationError.WithMessage(err.Error())
		}

		return processed, nil
	}

	if _, ok := value.(string); !ok {
		return value, nil
	}

	valueStr := value.(string)

	if strings.HasPrefix(valueStr, "$") {
		return m.evaluate(valueStr, variableMap)
	}

	return valueStr, nil
}

func (m module) executeCreate(ctx context.Context, execution *model2.TestExecution, step model2.TestCaseTestCaseStep, variableMap *map[string]interface{}) errors.ServiceError {
	payload, err := m.resolveValue(*step.Payload.(*interface{}), variableMap)

	if err != nil {
		return err
	}

	res, err := m.apiInterface.Create(ctx, (payload).(unstructured.Unstructured))

	if step.Name != nil {
		(*variableMap)[*step.Name+"_result"] = res
	}

	return err
}

func (m module) executeUpdate(ctx context.Context, execution *model2.TestExecution, step model2.TestCaseTestCaseStep, variableMap *map[string]interface{}) errors.ServiceError {
	payload, err := m.resolveValue(*step.Payload.(*interface{}), variableMap)

	if err != nil {
		return err
	}

	res, err := m.apiInterface.Update(ctx, (payload).(unstructured.Unstructured))

	if step.Name != nil {
		(*variableMap)[*step.Name+"_result"] = res
	}

	return err
}

func (m module) executeDelete(ctx context.Context, execution *model2.TestExecution, step model2.TestCaseTestCaseStep, variableMap *map[string]interface{}) errors.ServiceError {
	payload, err := m.resolveValue(*step.Payload.(*interface{}), variableMap)

	if err != nil {
		return err
	}

	return m.apiInterface.Delete(ctx, (payload).(unstructured.Unstructured))
}

func (m module) executeGet(ctx context.Context, execution *model2.TestExecution, step model2.TestCaseTestCaseStep, variableMap *map[string]interface{}) errors.ServiceError {
	payload, err := m.resolveValue(*step.Payload.(*interface{}), variableMap)

	if err != nil {
		return err
	}

	res, err := m.apiInterface.Load(ctx, payload.(unstructured.Unstructured), api.LoadParams{})

	if step.Name != nil {
		(*variableMap)[*step.Name+"_result"] = res
	}

	return err
}

func (m module) executeApply(ctx context.Context, execution *model2.TestExecution, step model2.TestCaseTestCaseStep, variableMap *map[string]interface{}) errors.ServiceError {
	payload, err := m.resolveValue(*step.Payload.(*interface{}), variableMap)

	if err != nil {
		return err
	}

	res, err := m.apiInterface.Apply(ctx, (payload).(unstructured.Unstructured))

	if step.Name != nil {
		(*variableMap)[*step.Name+"_result"] = res
	}

	return err
}

func (m module) executeNano(ctx context.Context, execution *model2.TestExecution, step model2.TestCaseTestCaseStep, variableMap *map[string]interface{}) errors.ServiceError {
	panic("not implement me")
}

func (m module) executeList(ctx context.Context, execution *model2.TestExecution, step model2.TestCaseTestCaseStep, variableMap *map[string]interface{}) errors.ServiceError {
	payload, err := m.resolveValue(*step.Payload.(*interface{}), variableMap)

	if err != nil {
		return err
	}

	if _, ok := payload.(unstructured.Unstructured); !ok {
		return errors.RecordValidationError.WithMessage("On List operation payload must be an an object")
	}

	payloadObj := payload.(unstructured.Unstructured)

	var params = api.ListParams{}

	if payloadObj["filters"] != nil {
		if _, ok := payloadObj["filters"].(map[string]string); !ok {
			return errors.RecordValidationError.WithMessage("On List operation payload.filters must be an an object")
		}
		params.Filters = payloadObj["filters"].(map[string]string)
	}

	if payloadObj["type"] != nil {
		if _, ok := payloadObj["type"].(string); !ok {
			return errors.RecordValidationError.WithMessage("On List operation payload.type must be an an string")
		}
		params.Type = payloadObj["type"].(string)
	} else {
		return errors.RecordValidationError.WithMessage("On List operation payload.type must be provided")
	}

	if payloadObj["limit"] != nil {
		if _, ok := payloadObj["limit"].(float64); !ok {
			return errors.RecordValidationError.WithMessage("On List operation payload.limit must be an an number")
		}
		params.Limit = uint32(payloadObj["limit"].(float64))
	}

	if payloadObj["offset"] != nil {
		if _, ok := payloadObj["offset"].(float64); !ok {
			return errors.RecordValidationError.WithMessage("On List operation payload.offset must be an an number")
		}
		params.Offset = uint64(payloadObj["offset"].(float64))
	}

	res, err := m.apiInterface.List(ctx, params)

	if step.Name != nil {
		(*variableMap)[*step.Name+"_result"] = res.Content
		(*variableMap)[*step.Name+"_result_total"] = res.Total
	}

	return err
}

func (m module) evaluate(expr string, variableMap *map[string]interface{}) (interface{}, errors.ServiceError) {
	// it is recursively evulating the string
	// e.g.
	// $test_case_result.id => It will return the id of the test case result

	expr = strings.TrimPrefix(expr, "$")

	if strings.Contains(expr, ".") {
		left := expr[0:strings.Index(expr, ".")]
		right := expr[strings.Index(expr, ".")+1:]

		// if left is a variable
		if (*variableMap)[left] != nil {
			if _, ok := (*variableMap)[left].(map[string]interface{}); !ok {
				return nil, errors.RecordValidationError.WithMessage(fmt.Sprintf("Variable %s is not an object", left))
			}
			var newVars = (*variableMap)[left].(map[string]interface{})

			return m.evaluate(right, &newVars)
		} else {
			return nil, errors.RecordValidationError.WithMessage(fmt.Sprintf("Variable %s not found", left))
		}
	} else {
		if (*variableMap)[expr] != nil {
			return (*variableMap)[expr], nil
		}
	}

	return nil, errors.RecordValidationError.WithMessage(fmt.Sprintf("Variable %s not found", expr))
}

func (m module) log(execution *model2.TestExecution, format string, args ...interface{}) {
	logStr := fmt.Sprintf(format, args...)

	log.Infof("[TESTING] %s: %s", execution.TestCase.Name, logStr)

	if execution.Logs != nil {
		execution.Logs = util.Pointer(fmt.Sprintf("%s\n%s", *execution.Logs, logStr))
	}
}

func NewModule(container service.Container) service.Module {
	backendEventHandler := container.GetBackendEventHandler().(backend_event_handler.BackendEventHandler)
	return &module{container: container, backendEventHandler: backendEventHandler, apiInterface: api.NewInterface(container)}
}

// Record Validation failed - (propInt32:value is not int32: aa) (propInt64:value is not int64: bb) (propFloat32:value is not bool: cc) (propFloat64:value is not bool: dd)
// Record Validation failed - (propFloat32:value is not bool: cc) (propFloat64:value is not bool: dd) (propInt32:value is not int32: aa) (propInt64:value is not int64: bb)
