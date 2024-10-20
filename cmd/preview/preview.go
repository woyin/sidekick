/*
Copyright © 2024 Mahmoud Mousa <m.mousa@hey.com>

Licensed under the GNU GPL License, Version 3.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
https://www.gnu.org/licenses/gpl-3.0.en.html

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package preview

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	previewList "github.com/mightymoud/sidekick/cmd/preview/list"
	"github.com/mightymoud/sidekick/utils"
	"github.com/pterm/pterm"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

var PreviewCmd = &cobra.Command{
	Use:   "preview",
	Short: "Deploy a preview environment for your application",
	Long:  `Sidekick allows you to deploy preview environment based on commit hash`,
	Run: func(cmd *cobra.Command, args []string) {
		if configErr := utils.ViperInit(); configErr != nil {
			pterm.Error.Println("Sidekick config not found - Run sidekick init")
			os.Exit(1)
		}
		appConfig, appConfigErr := utils.LoadAppConfig()
		if appConfigErr != nil {
			log.Fatalln("Unable to load your config file. Might be corrupted")
			os.Exit(1)
		}

		gitTreeCheck := exec.Command("sh", "-s", "-")
		gitTreeCheck.Stdin = strings.NewReader(utils.CheckGitTreeScript)
		output, _ := gitTreeCheck.Output()
		if string(output) != "all good\n" {
			fmt.Println(string(output))
			pterm.Error.Println("Please commit any changes to git before deploying a preview environment")
			os.Exit(1)
		}

		gitShortHashCmd := exec.Command("sh", "-s", "-")
		gitShortHashCmd.Stdin = strings.NewReader("git rev-parse --short HEAD")
		hashOutput, hashErr := gitShortHashCmd.Output()
		if hashErr != nil {
			panic(hashErr)
		}
		deployHash := string(hashOutput)
		deployHash = strings.TrimSuffix(deployHash, "\n")

		pterm.Println()
		pterm.DefaultHeader.WithFullWidth().Println("Deploying a preview env of your app 😎")
		pterm.Println()

		multi := pterm.DefaultMultiPrinter
		loginSpinner, _ := utils.GetSpinner().WithWriter(multi.NewWriter()).Start("Logging into VPS")
		dockerBuildStageSpinner, _ := utils.GetSpinner().WithWriter(multi.NewWriter()).Start("Building latest docker image of your app")
		deployStageSpinner, _ := utils.GetSpinner().WithWriter(multi.NewWriter()).Start("Deploying a preview env of your application")

		multi.Start()

		loginSpinner.Sequence = []string{"▀ ", " ▀", " ▄", "▄ "}
		sshClient, err := utils.Login(viper.Get("serverAddress").(string), "sidekick")
		if err != nil {
			loginSpinner.Fail("Something went wrong logging in to your VPS")
			panic(err)
		}
		loginSpinner.Success("Logged in successfully!")

		dockerBuildStageSpinner.Sequence = []string{"▀ ", " ▀", " ▄", "▄ "}

		envVariables := []string{}
		dockerEnvProperty := []string{}
		envFileChecksum := ""
		if appConfig.Env.File != "" {
			envErr := utils.HandleEnvFile(appConfig.Env.File, envVariables, &dockerEnvProperty, &envFileChecksum)
			if envErr != nil {
				panic(envErr)
			}
		}
		defer os.Remove("encrypted.env")

		imageName := fmt.Sprintf("%s:%s", appConfig.Name, deployHash)
		serviceName := fmt.Sprintf("%s-%s", appConfig.Name, deployHash)
		previewURL := fmt.Sprintf("%s.%s", deployHash, appConfig.Url)
		newService := utils.DockerService{
			Image: imageName,
			Labels: []string{
				"traefik.enable=true",
				fmt.Sprintf("traefik.http.routers.%s.rule=Host(`%s`)", serviceName, previewURL),
				fmt.Sprintf("traefik.http.services.%s.loadbalancer.server.port=%s", serviceName, fmt.Sprint(appConfig.Port)),
				fmt.Sprintf("traefik.http.routers.%s.tls=true", serviceName),
				fmt.Sprintf("traefik.http.routers.%s.tls.certresolver=default", serviceName),
				"traefik.docker.network=sidekick",
			},
			Environment: dockerEnvProperty,
			Networks: []string{
				"sidekick",
			},
		}
		newDockerCompose := utils.DockerComposeFile{
			Services: map[string]utils.DockerService{
				serviceName: newService,
			},
			Networks: map[string]utils.DockerNetwork{
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

		cwd, _ := os.Getwd()
		var stdErrBuff bytes.Buffer
		dockerBuildCommd := exec.Command("sh", "-s", "-", appConfig.Name, cwd, deployHash)
		dockerBuildCommd.Stdin = strings.NewReader(utils.DockerBuildAndSaveScript)
		dockerBuildCommd.Stderr = &stdErrBuff
		if dockerBuildErr := dockerBuildCommd.Run(); dockerBuildErr != nil {
			pterm.Error.Printfln("Failed to build docker image with the following error: \n%s", stdErrBuff.String())
			os.Exit(1)
		}

		imgMoveCmd := exec.Command("sh", "-s", "-", appConfig.Name, "sidekick", viper.GetString("serverAddress"), deployHash)
		imgMoveCmd.Stdin = strings.NewReader(utils.ImageMoveScript)
		_, imgMoveErr := imgMoveCmd.Output()
		if imgMoveErr != nil {
			log.Fatalf("Issue occurred with moving image to your VPS: %s", imgMoveErr)
			os.Exit(1)
		}
		if _, _, sessionErr := utils.RunCommand(sshClient, fmt.Sprintf("cd %s && docker load -i %s-%s.tar", appConfig.Name, appConfig.Name, deployHash)); sessionErr != nil {
			log.Fatal("Issue happened loading docker image")
		}
		if _, _, sessionErr := utils.RunCommand(sshClient, fmt.Sprintf("cd %s && rm %s", appConfig.Name, fmt.Sprintf("%s-%s.tar", appConfig.Name, deployHash))); sessionErr != nil {
			log.Fatal("Issue happened cleaning up the image file")
		}

		dockerBuildStageSpinner.Success("Successfully built and pushed docker image")

		deployStageSpinner.Sequence = []string{"▀ ", " ▀", " ▄", "▄ "}
		_, _, sessionErr0 := utils.RunCommand(sshClient, fmt.Sprintf(`mkdir -p %s/preview/%s`, appConfig.Name, deployHash))
		if sessionErr0 != nil {
			panic(sessionErr0)
		}
		rsync := exec.Command("rsync", "docker-compose.yaml", fmt.Sprintf("%s@%s:%s", "sidekick", viper.Get("serverAddress").(string), fmt.Sprintf("./%s/preview/%s", appConfig.Name, deployHash)))
		rsync.Run()
		if appConfig.Env.File != "" {
			encryptSync := exec.Command("rsync", "encrypted.env", fmt.Sprintf("%s@%s:%s", "sidekick", viper.Get("serverAddress").(string), fmt.Sprintf("./%s/preview/%s", appConfig.Name, deployHash)))
			encryptSync.Run()

			_, _, sessionErr1 := utils.RunCommand(sshClient, fmt.Sprintf(`cd %s/preview/%s && sops exec-env encrypted.env 'docker compose -p sidekick up -d'`, appConfig.Name, deployHash))
			if sessionErr1 != nil {
				panic(sessionErr1)
			}
		} else {
			_, _, sessionErr1 := utils.RunCommand(sshClient, fmt.Sprintf(`cd %s/preview/%s && docker compose -p sidekick up -d`, appConfig.Name, deployHash))
			if sessionErr1 != nil {
				panic(sessionErr1)
			}
		}
		previewEnvConfig := utils.SidekickPreview{
			Url:       fmt.Sprintf("https://%s", previewURL),
			Image:     imageName,
			CreatedAt: time.Now().Format(time.UnixDate),
		}
		appConfig.PreviewEnvs = map[string]utils.SidekickPreview{}
		appConfig.PreviewEnvs[deployHash] = previewEnvConfig

		ymlData, _ := yaml.Marshal(&appConfig)
		os.WriteFile("./sidekick.yml", ymlData, 0644)

		deployStageSpinner.Success("Successfully built and pushed docker image")
		multi.Stop()

		pterm.Println()
		pterm.Info.Printfln("😎 Access your preview app at: https://%s.%s", deployHash, appConfig.Url)
		pterm.Println()

	},
}

func init() {
	PreviewCmd.AddCommand(previewList.ListCmd)
}
