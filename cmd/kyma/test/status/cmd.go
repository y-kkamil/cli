package status

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	oct "github.com/kyma-incubator/octopus/pkg/apis/testing/v1alpha1"
	"github.com/kyma-project/cli/internal/cli"
	"github.com/kyma-project/cli/internal/kube"
	"github.com/kyma-project/cli/pkg/api/octopus"
	"github.com/kyma-project/cli/cmd/kyma/test"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type command struct {
	opts *options
	cli.Command
}

func NewCmd(o *options) *cobra.Command {
	cmd := command{
		Command: cli.Command{Options: o.Options},
		opts:    o,
	}

	cobraCmd := &cobra.Command{
		Use:   "status <test-suite-1> <test-suite-2> ... <test-suite-N>",
		Short: "Shows the status of a test suite and it's related test executions",
		Long: `Use this command to display the status of a test suite and related test executions.

If you don't provide any arguments, the status of all test suites will be printed.

To print the status of all test suites, run:

    kyma test status

To print the status of specific test cases, run:

	kyma test status testSuiteOne testSuiteTwo`,
		RunE:    func(_ *cobra.Command, args []string) error { return cmd.Run(args) },
		Aliases: []string{"s"},
	}

	cobraCmd.Flags().StringVarP(&o.OutputFormat, "output", "o", "",
		"Output format. One of: json|yaml|wide")
	cobraCmd.Flags().Bool("help", false, "Displays help for the command.")
	return cobraCmd
}

func (cmd *command) Run(args []string) error {
	var err error
	if cmd.K8s, err = kube.NewFromConfig("", cmd.KubeconfigPath); err != nil {
		return errors.Wrap(err, "Could not initialize the Kubernetes client. Make sure that your kubeconfig is valid.")
	}

	switch len(args) {
	case 1:
		testSuite, err := cmd.K8s.Octopus().GetTestSuite(args[0], metav1.GetOptions{})
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("unable to get test suite '%s'",
				args[0]))
		}
		return cmd.printTestSuiteStatus(testSuite, cmd.opts.OutputFormat)
	case 0:
		testList, err := cmd.K8s.Octopus().ListTestSuites(metav1.ListOptions{})
		if err != nil {
			return errors.Wrap(err, "unable to list test suites")
		}

		if len(testList.Items) == 0 {
			fmt.Println("no test suites found")
			return nil
		}

		for _, t := range testList.Items {
			if err := cmd.printTestSuiteStatus(&t, cmd.opts.OutputFormat); err != nil {
				return err
			}
		}
	default:
		testsList, err := listTestSuitesByName(cmd.K8s.Octopus(), args)
		if err != nil {
			return errors.Wrap(err, "unable to list test suites")
		}

		for _, t := range testsList {
			if err := cmd.printTestSuiteStatus(&t, cmd.opts.OutputFormat); err != nil {
				return err
			}
		}
	}
	return nil
}

func (cmd *command) printTestSuiteStatus(testSuite *oct.ClusterTestSuite, outputFormat string) error {
	switch strings.ToLower(outputFormat) {
	case "yaml":
		d, err := yaml.Marshal(testSuite)
		if err != nil {
			return errors.Wrap(err,
				fmt.Sprintf("Unable to marshal test suite '%s' to yaml",
					testSuite.GetName()))
		}
		fmt.Println(string(d))
		return nil
	case "json":
		d, err := json.MarshalIndent(testSuite, "", "\t")
		if err != nil {
			return errors.Wrap(err,
				fmt.Sprintf("Unable to marshal test suite '%s' to json",
					testSuite.GetName()))
		}
		fmt.Println(string(d))
		return nil
	case "wide":
		printTestSuite(testSuite, true)
	default:
		printTestSuite(testSuite, false)
	}

	return nil
}

func printTestSuite(testSuite *oct.ClusterTestSuite, wide bool) {
	fmt.Printf("Name:\t\t%s\r\n", testSuite.GetName())
	fmt.Printf("Concurrency:\t%d\r\n", testSuite.Spec.Concurrency)
	fmt.Printf("MaxRetries:\t%d\r\n", testSuite.Spec.MaxRetries)
	if testSuite.Status.StartTime != nil {
		fmt.Printf("StartTime:\t%s\r\n", testSuite.Status.StartTime.String())
	} else {
		fmt.Printf("StartTime:\t%s\r\n", "Not started yet")
	}
	if testSuite.Status.CompletionTime != nil {
		fmt.Printf("EndTime:\t%s\r\n", testSuite.Status.CompletionTime)
	} else {
		fmt.Printf("EndTime:\t%s\r\n", "Not finished yet")
	}

	fmt.Printf("Condition:\t%s\r\n", testSuite.Status.Conditions[len(testSuite.Status.Conditions)-1].Type)

	writer := test.NewTableWriter([]string{}, os.Stdout)
	for _, t := range testSuite.Status.Results {

		if wide {
			writer.SetHeader([]string{"TEST", "STATUS", "NAMESPACE", "POD"})
			var podName string
			switch len(t.Executions) {
			case 0:
				break
			case 1:
				podName = t.Executions[0].ID
			default:
				podName = t.Executions[len(t.Executions)-1].ID
			}
			writer.Append([]string{t.Name, string(t.Status), t.Namespace, podName})
		} else {
			writer.Append([]string{t.Name, string(t.Status)})
		}
	}
	fmt.Printf("Completed:\t%d/%d\r\n",
		test.GetNumberOfFinishedTests(testSuite),
		len(testSuite.Status.Results))
	writer.Render()
	if rc := generateRerunCommand(testSuite); rc != "" {
		fmt.Println("Rerun failed tests:", rc)
	}
}

func generateRerunCommand(testSuite *oct.ClusterTestSuite) string {
	failedDefs := []string{}
	for _, t := range testSuite.Status.Results {
		if t.Status == oct.TestFailed {
			failedDefs = append(failedDefs, t.Name)
		}
	}
	if len(failedDefs) == 0 {
		return ""
	}

	var result string
	result = fmt.Sprintf("kyma test run %s", strings.Join(failedDefs, " "))
	if testSuite.Spec.Concurrency != 1 {
		result += fmt.Sprintf(" --concurrency=%d", testSuite.Spec.Concurrency)
	}
	if testSuite.Spec.MaxRetries != 1 {
		result += fmt.Sprintf(" --max-retries=%d", testSuite.Spec.MaxRetries)
	}
	if testSuite.Spec.Count != 1 {
		result += fmt.Sprintf(" --count=%d", testSuite.Spec.Count)
	}
	return result
}

func listTestSuitesByName(cli octopus.OctopusInterface, names []string) ([]oct.ClusterTestSuite, error) {
	suites, err := cli.ListTestSuites(metav1.ListOptions{})
	if err != nil {
		return nil, errors.Wrap(err, "Unable to list test suites")
	}

	result := []oct.ClusterTestSuite{}
	for _, suite := range suites.Items {
		for _, tName := range names {
			if suite.ObjectMeta.Name == tName {
				result = append(result, suite)
			}
		}
	}
	return result, nil
}
