package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"neuroforge/internal/transport"
)

// This file implements the pipeline inspection/control commands:
//
//	forge pipeline status <task-id>   — durable run state + stage history
//	forge pipeline cancel <task-id>   — durable, idempotent cancel
//	forge estop on|off|status         — emergency stop (persists across restarts)

// runPipelineCmd implements `forge pipeline status|cancel <task-id>`.
func (a *App) runPipelineCmd(args []string) int {
	if len(args) < 2 || (args[0] != "status" && args[0] != "cancel") {
		fmt.Fprintln(a.Err, "usage: forge pipeline status <task-id> | forge pipeline cancel <task-id>")
		return ExitValidation
	}
	sub, taskID := args[0], args[1]

	cli, err := a.ensureDaemon()
	if err != nil {
		fmt.Fprintf(a.Err, "forge: daemon start failed: %v\n", err)
		return ExitInfra
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var res transport.PipelineRunResultDTO
	switch sub {
	case "status":
		res, err = cli.PipelineStatus(ctx, taskID)
	case "cancel":
		res, err = cli.CancelPipeline(ctx, taskID)
	}
	if err != nil {
		fmt.Fprintf(a.Err, "forge: pipeline %s: %v\n", sub, err)
		return ExitErr
	}

	if sub == "cancel" {
		fmt.Fprintf(a.Out, "Pipeline run for task %s: %s\n", res.TaskID, res.RunState)
		if res.Outcome != "" {
			fmt.Fprintf(a.Out, "Outcome: %s\n", res.Outcome)
		}
		return ExitOK
	}

	// status
	fmt.Fprintf(a.Out, "Task:         %s\n", res.TaskID)
	fmt.Fprintf(a.Out, "Run state:    %s\n", res.RunState)
	fmt.Fprintf(a.Out, "Current stage:%s\n", res.CurrentStage)
	if res.Outcome != "" {
		fmt.Fprintf(a.Out, "Outcome:      %s\n", res.Outcome)
	}
	if res.ResultRef != "" {
		fmt.Fprintf(a.Out, "Result ref:   %s\n", res.ResultRef)
	}
	if res.FailureCategory != "" {
		fmt.Fprintf(a.Out, "Failure:      %s: %s\n", res.FailureCategory, res.FailureReason)
	}
	a.emitStageSummary(res)
	return ExitOK
}

// runEstopCmd implements `forge estop on|off|status`.
func (a *App) runEstopCmd(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(a.Err, "usage: forge estop on [reason...] | forge estop off | forge estop status")
		return ExitValidation
	}
	sub := args[0]
	if sub != "on" && sub != "off" && sub != "status" {
		fmt.Fprintf(a.Err, "forge: unknown estop action %q (want on|off|status)\n", sub)
		return ExitValidation
	}

	cli, err := a.ensureDaemon()
	if err != nil {
		fmt.Fprintf(a.Err, "forge: daemon start failed: %v\n", err)
		return ExitInfra
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch sub {
	case "status":
		st, err := cli.EmergencyStopStatus(ctx)
		if err != nil {
			fmt.Fprintf(a.Err, "forge: estop status: %v\n", err)
			return ExitErr
		}
		if st.On {
			fmt.Fprintf(a.Out, "emergency stop: ON (%s)\n", st.Reason)
		} else {
			fmt.Fprintln(a.Out, "emergency stop: off")
		}
		return ExitOK
	case "on", "off":
		on := sub == "on"
		reason := ""
		if on && len(args) > 1 {
			reason = strings.Join(args[1:], " ")
		}
		st, err := cli.SetEmergencyStop(ctx, on, reason)
		if err != nil {
			fmt.Fprintf(a.Err, "forge: estop %s: %v\n", sub, err)
			return ExitErr
		}
		if st.On {
			fmt.Fprintf(a.Out, "emergency stop engaged (%s): in-flight agent runs cancelled; new runs refused\n", st.Reason)
		} else {
			fmt.Fprintln(a.Out, "emergency stop cleared: queued runs resume on next daemon start")
		}
		return ExitOK
	}
	return ExitValidation
}
