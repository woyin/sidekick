/*
Copyright © 2024 Mahmoud Mosua <m.mousa@hey.com>

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
*/
package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ms-mousa/sidekick/utils"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

var launchCmd = &cobra.Command{
	Use:   "launch",
	Short: "Launch a new application to host on your VPS with Sidekick",
	Long:  `This command will run you through the basic setup to add a new application to your VPS.`,
	Run: func(cmd *cobra.Command, args []string) {

		viper.SetConfigName("sidekick")
		viper.SetConfigType("yaml")
		viper.AddConfigPath("$HOME/.config/sidekick/")
		err := viper.ReadInConfig()
		if err != nil {
			panic(fmt.Errorf("fatal error config file: %w", err))
		}

		keyAddSshCommand := exec.Command("sh", "-s", "-", viper.Get("serverAddress").(string))
		keyAddSshCommand.Stdin = strings.NewReader(utils.SshKeysScript)
		if sshAddErr := keyAddSshCommand.Run(); sshAddErr != nil {
			panic(sshAddErr)
		}

		if utils.FileExists("./dockerfile") {
			pterm.Info.Println("Dockerfile detected - scanning file for details")
		} else {
			pterm.Error.Println("No dockerfiles found in current directory.")
		}
		pterm.Info.Println("Analyzing docker file...")
		res, err := os.ReadFile("./Dockerfile")
		if err != nil {
			pterm.Error.Println("Unable to process your dockerfile")
		}
		// attempt to get a port from dockerfile
		appPort := ""
		for _, line := range strings.Split(string(res), "\n") {
			if strings.HasPrefix(line, "EXPOSE") {
				appPort = line[len(line)-4:]
			}
		}

		appName := ""
		appNameTextInput := pterm.DefaultInteractiveTextInput
		appNameTextInput.DefaultText = "Please enter your app url friendly app name"
		appName, _ = appNameTextInput.Show()
		if appName == "" || strings.Contains(appName, " ") {
			pterm.Error.Println("You have to enter url friendly app name")
			os.Exit(0)
		}

		appPortTextInput := pterm.DefaultInteractiveTextInput.WithDefaultValue(appPort)
		appPortTextInput.DefaultText = "Please enter the port at which the app receives request"
		appPort, _ = appPortTextInput.Show()
		if appPort == "" {
			pterm.Error.Println("You you have to enter a port to accept requests")
			os.Exit(0)
		}

		appDomain := ""
		appDomainTextInput := pterm.DefaultInteractiveTextInput.WithDefaultValue(fmt.Sprintf("%s.%s.sslip.io", appName, viper.Get("serverAddress").(string)))
		appDomainTextInput.DefaultText = "Please enter the domain to point the app to"
		appDomain, _ = appDomainTextInput.Show()

		// make a docker service
		imageName := fmt.Sprintf("%s/%s", viper.Get("dockerUsername").(string), appName)
		newService := DockerService{
			Image: imageName,
			Labels: []string{
				"traefik.enable=true",
				fmt.Sprintf("traefik.http.routers.%s.rule=Host(`%s`)", appName, appDomain),
				fmt.Sprintf("traefik.http.services.%s.loadbalancer.server.port=%s", appName, appPort),
				fmt.Sprintf("traefik.http.routers.%s.tls=true", appName),
				fmt.Sprintf("traefik.http.routers.%s.tls.certresolver=default", appName),
				"traefik.docker.network=sidekick",
			},
			Networks: []string{
				"sidekick",
			},
		}
		newDockerCompose := DockerComposeFile{
			Services: map[string]DockerService{
				appName: newService,
			},
			Networks: map[string]DockerNetwork{
				"sidekick": {
					External: true,
				},
			},
		}
		dockerComposeFile, err := yaml.Marshal(&newDockerCompose)
		if err != nil {
			fmt.Printf("Error marshalling YAML: %v\n", err)
			return
		}
		err = os.WriteFile("docker-compose.yaml", dockerComposeFile, 0644)
		if err != nil {
			fmt.Printf("Error writing file: %v\n", err)
			return
		}
		defer os.Remove("docker-compose.yaml")

		multi := pterm.DefaultMultiPrinter
		launchPb, _ := pterm.DefaultProgressbar.WithTotal(3).WithWriter(multi.NewWriter()).Start("Booting up app on VPS")
		loginSpinner, _ := utils.GetSpinner().WithWriter(multi.NewWriter()).Start("Logging into VPS")
		dockerBuildSpinner, _ := utils.GetSpinner().WithWriter(multi.NewWriter()).Start("Preparing docker image")
		setupSpinner, _ := utils.GetSpinner().WithWriter(multi.NewWriter()).Start("Setting up application")

		multi.Start()

		sshClient, err := utils.LoginStage(viper.Get("serverAddress").(string), loginSpinner, launchPb)
		if err != nil {
			panic(err)
		}
		launchPb.Increment()

		dockerBuildSpinner.Sequence = []string{"▀ ", " ▀", " ▄", "▄ "}
		cwd, _ := os.Getwd()
		dockerBuildCommd := exec.Command("sh", "-s", "-", appName, viper.Get("dockerUsername").(string), cwd)
		dockerBuildCommd.Stdin = strings.NewReader(utils.DockerHandleScript)
		if dockerBuildErr := dockerBuildCommd.Run(); dockerBuildErr != nil {
			panic(dockerBuildErr)
		}
		dockerBuildSpinner.Success("Successfully built and pushed docker image")
		launchPb.Increment()

		setupSpinner.Sequence = []string{"▀ ", " ▀", " ▄", "▄ "}
		sessionErr := utils.RunCommand(sshClient, fmt.Sprintf("mkdir %s", appName))
		if sessionErr != nil {
			panic(sessionErr)
		}
		rsync := exec.Command("rsync", "docker-compose.yaml", fmt.Sprintf("%s@%s:%s", "root", viper.Get("serverAddress").(string), fmt.Sprintf("./%s", appName)))
		rsync.Run()

		sessionErr1 := utils.RunCommand(sshClient, fmt.Sprintf("cd %s && docker compose -p sidekick up -d", appName))
		if sessionErr1 != nil {
			panic(sessionErr1)
		}
		// save app config in same folder
		sidekickAppConfig := SidekickAppConfig{
			Image:          fmt.Sprintf("%s/%s", viper.Get("dockerUsername"), appName),
			Url:            appDomain,
			CreatedAt:      time.Now().Format(time.UnixDate),
			LastDeployedAt: time.Now().Format(time.UnixDate),
		}
		ymlData, err := yaml.Marshal(&sidekickAppConfig)
		os.WriteFile("sidekick.yml", ymlData, 0644)
		launchPb.Increment()

		setupSpinner.Success("App setup successfully")

		multi.Stop()
	},
}

func init() {
	rootCmd.AddCommand(launchCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// launchCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// launchCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}