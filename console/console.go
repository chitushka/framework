// Package console provides an extensible command application over the same
// lifecycle and component registry as web applications.
package console

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"

	frameworkapp "github.com/chitushka/framework/app"
)

type Context struct {
	Application *frameworkapp.Application
	Out         io.Writer
	Err         io.Writer
}
type Command interface {
	Name() string
	Description() string
	Run(context.Context, *Context, []string) error
}
type CommandFunc struct {
	CommandName        string
	CommandDescription string
	Execute            func(context.Context, *Context, []string) error
}

func (command CommandFunc) Name() string        { return command.CommandName }
func (command CommandFunc) Description() string { return command.CommandDescription }
func (command CommandFunc) Run(ctx context.Context, console *Context, args []string) error {
	if command.Execute == nil {
		return errors.New("console: command executor is required")
	}
	return command.Execute(ctx, console, args)
}

type Application struct {
	App      *frameworkapp.Application
	Out      io.Writer
	Err      io.Writer
	mu       sync.RWMutex
	commands map[string]Command
	running  bool
}

func New(application *frameworkapp.Application) *Application {
	if application == nil {
		application = frameworkapp.New(nil, nil)
	}
	return &Application{App: application, Out: os.Stdout, Err: os.Stderr, commands: make(map[string]Command)}
}
func (application *Application) Register(commands ...Command) error {
	if application == nil {
		return errors.New("console: application is nil")
	}
	application.mu.Lock()
	defer application.mu.Unlock()
	if application.running {
		return errors.New("console: commands cannot be registered after run")
	}
	for _, command := range commands {
		if command == nil {
			return errors.New("console: command is nil")
		}
		name := strings.TrimSpace(command.Name())
		if name == "" || name != command.Name() || strings.ContainsAny(name, " \t\r\n") {
			return fmt.Errorf("console: invalid command name %q", command.Name())
		}
		if _, exists := application.commands[name]; exists {
			return fmt.Errorf("console: command %q already registered", name)
		}
		application.commands[name] = command
	}
	return nil
}
func (application *Application) Run(ctx context.Context, args []string) error {
	if application == nil {
		return errors.New("console: application is nil")
	}
	if ctx == nil {
		return errors.New("console: context is nil")
	}
	application.mu.Lock()
	if application.running {
		application.mu.Unlock()
		return errors.New("console: application is already running")
	}
	application.running = true
	application.mu.Unlock()
	defer func() { application.mu.Lock(); application.running = false; application.mu.Unlock() }()
	if application.Out == nil {
		application.Out = io.Discard
	}
	if application.Err == nil {
		application.Err = io.Discard
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		return application.help(args)
	}
	application.mu.RLock()
	command := application.commands[args[0]]
	application.mu.RUnlock()
	if command == nil {
		return fmt.Errorf("console: unknown command %q", args[0])
	}
	if err := application.App.Start(ctx); err != nil {
		return err
	}
	commandContext := &Context{Application: application.App, Out: application.Out, Err: application.Err}
	commandErr := command.Run(ctx, commandContext, args[1:])
	shutdownErr := application.App.Shutdown(context.WithoutCancel(ctx))
	return errors.Join(commandErr, shutdownErr)
}
func (application *Application) help(args []string) error {
	if len(args) > 2 {
		return errors.New("console: help accepts at most one command")
	}
	if len(args) == 2 {
		application.mu.RLock()
		command := application.commands[args[1]]
		application.mu.RUnlock()
		if command == nil {
			return fmt.Errorf("console: unknown command %q", args[1])
		}
		fmt.Fprintf(application.Out, "%s\t%s\n", command.Name(), command.Description())
		return nil
	}
	application.mu.RLock()
	commands := make([]Command, 0, len(application.commands))
	for _, command := range application.commands {
		commands = append(commands, command)
	}
	application.mu.RUnlock()
	sort.Slice(commands, func(i, j int) bool { return commands[i].Name() < commands[j].Name() })
	writer := tabwriter.NewWriter(application.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "COMMAND\tDESCRIPTION")
	for _, command := range commands {
		fmt.Fprintf(writer, "%s\t%s\n", command.Name(), command.Description())
	}
	return writer.Flush()
}
