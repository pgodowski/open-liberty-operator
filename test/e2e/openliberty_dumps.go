package e2e

import (
	goctx "context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	openlibertyv1beta1 "github.com/OpenLiberty/open-liberty-operator/pkg/apis/openliberty/v1beta1"
	"github.com/OpenLiberty/open-liberty-operator/test/util"
	framework "github.com/operator-framework/operator-sdk/pkg/test"
	e2eutil "github.com/operator-framework/operator-sdk/pkg/test/e2eutil"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

// OpenLibertyDumpsTest tests functionalities related to dumps.
func OpenLibertyDumpsTest(t *testing.T) {
	ctx, err := util.InitializeContext(t, cleanupTimeout, retryInterval)
	if err != nil {
		t.Fatal(err)
	}
	defer ctx.Cleanup()

	f := framework.Global
	namespace, err := ctx.GetNamespace()
	if err != nil {
		t.Fatalf("could not get namespace: %v", err)
	}

	t.Logf("Namespace: %s", namespace)

	// wait for the operator
	err = e2eutil.WaitForOperatorDeployment(t, f.KubeClient, namespace, "open-liberty-operator", 1, retryInterval, operatorTimeout)
	if err != nil {
		util.FailureCleanup(t, f, namespace, err)
	}

	timestamp := time.Now().UTC()
	t.Logf("%s - Starting open liberty dumps test...", timestamp)

	// create one replica of the operator deployment in current namespace with provided name
	err = e2eutil.WaitForOperatorDeployment(t, f.KubeClient, namespace, "open-liberty-operator", 1, retryInterval, operatorTimeout)
	if err != nil {
		t.Fatal(err)
	}

	// set up OL to get dump from
	app := "example-liberty-dumps"
	// make basic open liberty application with 1 replica
	replicas := 1
	if err := createApp(t, f, ctx, app, replicas); err != nil {
		util.FailureCleanup(t, f, namespace, err)
	}

	// get the pods for the above app
	podList, err := util.GetPods(f, ctx, app, namespace)
	if err != nil {
		util.FailureCleanup(t, f, namespace, err)
	}

	err = createDump(t, f, ctx, podList)
	if err != nil {
		util.FailureCleanup(t, f, namespace, err)
	}

}

func createApp(t *testing.T, f *framework.Framework, ctx *framework.TestCtx, target string, replicas int) error {
	ns, err := ctx.GetNamespace()
	if err != nil {
		return err
	}

	openLibertyApplication := util.MakeBasicOpenLibertyApplication(t, f, target, ns, int32(replicas))
	// set up serviceability, prereq to dumps
	openLibertyApplication.Spec.Serviceability = &openlibertyv1beta1.OpenLibertyApplicationServiceability{
		Size: "1Gi",
	}

	// use TestCtx's create helper to create the object and add a cleanup function for the new object
	err = f.Client.Create(goctx.TODO(), openLibertyApplication, &framework.CleanupOptions{TestContext: ctx, Timeout: cleanupTimeout, RetryInterval: cleanupRetryInterval})
	if err != nil {
		util.FailureCleanup(t, f, ns, err)
	}

	// wait for example-liberty-dumps to reach 1 replicas
	err = e2eutil.WaitForDeployment(t, f.KubeClient, ns, target, replicas, retryInterval, timeout)
	if err != nil {
		util.FailureCleanup(t, f, ns, err)
	}

	return nil
}

func createDump(t *testing.T, f *framework.Framework, ctx *framework.TestCtx, pods *corev1.PodList) error {
	ns, err := ctx.GetNamespace()
	if err != nil {
		return fmt.Errorf("could not get namespace: %v", err)
	}

	podName := pods.Items[0].GetName()
	dump := util.MakeOpenLibertyDump(t, f, "test-dump", ns, podName)

	// use TestCtx's create helper to create the object and add a cleanup function for the new object
	err = f.Client.Create(goctx.TODO(), dump, &framework.CleanupOptions{TestContext: ctx, Timeout: cleanupTimeout, RetryInterval: cleanupRetryInterval})
	if err != nil {
		util.FailureCleanup(t, f, ns, err)
	}

	// wait for the dump to be created
	wait.Poll(retryInterval, timeout, func() (done bool, err error) {
		err = f.Client.Get(goctx.TODO(), types.NamespacedName{Name: "test-dump", Namespace: ns}, dump)
		if err != nil {
			t.Fatal(err)
		}

		for i := 0; i < len(dump.Status.Conditions); i++ {
			if dump.Status.Conditions[i].Type == "Completed" && dump.Status.Conditions[i].Status == "True" {
				return true, nil
			}
		}

		return false, nil
	})

	// checks the dump has started and completed with the correct status
	// checks the dump file is generated
	for i := 0; i < len(dump.Status.Conditions); i++ {
		if dump.Status.Conditions[i].Type == "Started" {
			if dump.Status.Conditions[i].Status != "True" {
				t.Fatalf("The Started State's Status is not True it is: %s, and the message is: %s", dump.Status.Conditions[i].Status, dump.Status.Conditions[i].Message)
			}
		} else if dump.Status.Conditions[i].Type == "Completed" {
			if dump.Status.Conditions[i].Status != "True" {
				t.Fatalf("The Completed State's Status is not True it is: %s, and the message is: %s", dump.Status.Conditions[i].Status, dump.Status.Conditions[i].Message)
			}
		}
	}

	t.Logf("The dumps status condition: %s", dump.Status.Conditions)
	// wait for file to be generated
	err = wait.Poll(retryInterval, timeout, func() (done bool, err error) {
		if dump.Status.DumpFile == "" {
			return false, nil
		}
		return true, nil
	})
	if errors.Is(err, wait.ErrWaitTimeout) {
		t.Log(dump.Status)
		t.Fatal("Dump file not created")
	}

	// get the dumps
	dir := "serviceability/" + ns
	out, err := exec.Command("kubectl", "exec", "-n", ns, "-it", podName, "--", "ls", dir, "-1t").Output()
	err = util.CommandError(t, err, out)
	if err != nil {
		t.Fatal("ls (1) command failed")
	}
	result := strings.Split(string(out), "\n")

	// get the zip file
	out, err = exec.Command("kubectl", "exec", "-n", ns, "-it", podName, "--", "ls", dir+"/"+result[0], "-1t").Output()
	err = util.CommandError(t, err, out)
	if err != nil {
		t.Fatal("ls (2) command failed")
	}
	zip := strings.Split(string(out), "\n")
	zipFile := zip[0]

	// copy the file to local machine
	localZipFile := strings.ReplaceAll(zipFile, ":", "")
	out, err = exec.Command("kubectl", "cp", ns+"/"+podName+":"+"serviceability/"+ns+"/"+podName+"/"+zipFile, localZipFile).Output()
	err = util.CommandError(t, err, out)
	if err != nil {
		t.Fatal("kubectl cp command failed")
	}

	// check if the zip file exists on local machine
	cmd := "ls | grep " + localZipFile
	out, err = exec.Command("bash", "-c", cmd).Output()
	err = util.CommandError(t, err, out)
	if err != nil {
		t.Fatal("ls (3) command failed")
	}
	if len(out) == 0 {
		t.Fatal("The zip file was not copied to the local machine")
	}

	// list all the files in the zip folder
	out, err = exec.Command("unzip", "-l", localZipFile).Output()
	err = util.CommandError(t, err, out)
	if err != nil {
		t.Fatal("unzip command failed")
	}

	// check if heap file is created
	cmdHeap := "unzip -l " + localZipFile + " | grep heapdump"
	heap, err := exec.Command("bash", "-c", cmdHeap).Output()
	err = util.CommandError(t, err, heap)
	if err != nil {
		t.Fatal("Heap file is not found")
	}
	if heap != nil {
		t.Logf("Heap file is found: %s", string(heap))
	} else {
		t.Fatal("Heap file not found")
	}

	// check if thread file is created
	cmdThread := "unzip -l " + localZipFile + " | grep javacore"
	thread, err := exec.Command("bash", "-c", cmdThread).Output()
	err = util.CommandError(t, err, thread)
	if err != nil {
		t.Fatal("Thread file is not found")
	}
	if thread != nil {
		t.Logf("Thread file is found: %s", string(thread))
	} else {
		t.Fatal("Thread file not found")
	}

	t.Log("Dump found!")

	// remove zip file
	clean, err := exec.Command("rm", localZipFile).Output()
	if err != nil {
		t.Fatalf("Unable to rm zip file: %s", clean)
	} else {
		t.Log("Deleted zip file")
	}

	return nil
}
