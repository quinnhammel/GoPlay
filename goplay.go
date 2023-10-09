package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// TODO: consider permissions.
// TODO: also make sure opened files are closed in defer.

const helpMessage = `GoPlay Use: 
1: 'goplay', creates a new directory under goplay directory. Goplay directory is set by environment variable GOPLAY_DIR, or defaults to '~/.goplay'. Then, opens the directory using your code editor command; this is set in GOPLAY_CODE_CMD, and defaults to 'code'.
2: 'goplay name', creates a new directory named 'name', as above (without a name provided, it makes it a uuid.) Name cannot be an integer.
3: 'goplay -d', deletes most recent goplay directory.
4: 'goplay -d name', deletes directory named 'name'
5: 'goplay -d 2', deletes last 2 directories; name cannot be an integer.
6: 'goplay -D', deletes all directories, with confirmation.
7: 'goplay --help', displays this help information.`

const programContents = `package main

import (
	"fmt"
)

func main() {
	fmt.Println("Hello world")
}
`

// Default will get home added to front of it. Cannot just use ~
const defaultDir = ".goplay"
const defaultCodeCMD = "code"

func getHomeDir() string {
	if dir := os.Getenv("GOPLAY_DIR"); dir != "" {
		return dir
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	return path.Join(homeDir, defaultDir)
}

func getCodeCMD() string {
	if cmd := os.Getenv("GOPLAY_CODE_CMD"); cmd != "" {
		return cmd
	}
	return defaultCodeCMD
}

// Following ensures setup and returns the file tracking created directories. Returned file can be appended to or read.
// It is callers responsibility to close returned file.
func setupHomeDir(dir string) (*os.File, error) {
	// Need to check if directory exists. If it does not, create it.
	// Then, check that the .generated_dirs file exists. If not, create it.
	// Easiest to just try to make dir and check for error.
	if err := os.Mkdir(dir, 0777); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}

	// Now we want to open and return the file.
	filePath := path.Join(dir, ".generated_dirs")
	// If the file doesn't exist, create it, or append to the file
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0777) // Will be closed later.
	if err != nil {
		panic(err)
	}
	return f, nil
}

// Following assumes that the homeDir is set up and the genFiles list has been passed in correctly.
// Following creates the directory under the home directory. Need to make the directory, add the name to the .generated_dirs file, and add a marker file in the directory (for ensuring do not delete wrong folder later). Then run go mod init on the directory, and run code commands on the path to open up editor.
func createPlaygroundDir(homeDir string, name string, genFilesList *os.File) error {
	if _, err := strconv.Atoi(name); err == nil {
		return fmt.Errorf("could not create playground \"%s\"; name cannot be an integer", name)
	}

	if name == "" {
		name = uuid.New().String()
	}
	dirPath := path.Join(homeDir, name)
	// Make the new directory.
	err := os.Mkdir(dirPath, 0777)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}

	// Now, we are running go mod init, adding the main file, and running code command on it.
	if err := os.Chdir(dirPath); err != nil {
		return err
	}
	// Now that the directory is made, we want to ensure it can be deleted later, even if the go mod init fails.
	defer func() {
		// Append the name to the file list.
		if _, err := genFilesList.Seek(0, io.SeekEnd); err != nil {
			panic(fmt.Sprintf("seek error: %s", err.Error()))
		}
		genFilesList.Write([]byte(fmt.Sprintf("%s\n", dirPath))) // Do not care about error.
		markerFileName := path.Join(dirPath, ".goplay_marker")
		markerFile, _ := os.OpenFile(markerFileName, os.O_RDONLY|os.O_CREATE, 0777) // Consider default permissions.
		markerFile.Close()
	}()
	goModCMD := exec.Command("go", "mod", "init", "main")
	goModCMD.Run() // Do not care about this error because it can happen on using old directory.

	// Adding main file.
	file, err := os.OpenFile(path.Join(dirPath, "main.go"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0777)
	if err == nil {
		defer file.Close()
		// Ok to write to the file.
		if _, err := io.WriteString(file, programContents); err != nil {
			return err
		}
	} else {
		if !errors.Is(err, os.ErrExist) {
			return err // We expect an exists err, but no others.
		}
	}
	// The code cmd is not as simple as you would think. We want to call code on the directory && the file, so it opens up.
	codeCMDArgs := strings.Split(strings.TrimSpace(getCodeCMD()), " ")
	dirCodeCMDArgs := append(codeCMDArgs, dirPath)
	fileCodeCMDArgs := append(codeCMDArgs, path.Join(dirPath, "main.go"))
	dirCodeCMD := exec.Command(dirCodeCMDArgs[0], dirCodeCMDArgs[1:]...) // Safe indices since these args were appended to.
	fileCodeCMD := exec.Command(fileCodeCMDArgs[0], fileCodeCMDArgs[1:]...)
	if err := dirCodeCMD.Run(); err != nil {
		return err
	}
	if err := fileCodeCMD.Run(); err != nil {
		return err
	}

	return nil
}

// Feed in the file for the list of generated files.
func deletePlaygroundDirs(genFilesList *os.File, homeDir string, nameOrNumber string, deleteAll bool) {
	// Some checks happen if deleteAll is false.
	toDelete := []string{} // File names, where file name is full path.
	isNumber := false
	num := 0
	if !deleteAll {
		// First check if this is a number. If it is, it must be positive (remember, names for playgrounds cannot be integers).
		if i, err := strconv.Atoi(nameOrNumber); err == nil {
			// It is a number. Must check if it is not positive.
			if i <= 0 {
				fmt.Printf("could not delete any dirs; provided number %d was not positive\n", i)
				return
			}
			// A valid number.
			isNumber = true
			num = i
		}
	}

	// Now, if it is not a number, and we are not deleting all, we assume it is a name. Try to delete it.
	if !deleteAll && !isNumber {
		toDelete = append(toDelete, path.Join(homeDir, nameOrNumber))
	}

	// Want to construct toDelete if we have not
	_, err := genFilesList.Seek(0, io.SeekStart)
	if err != nil {
		panic(err)
	}
	lines := make([]string, 0)
	scanner := bufio.NewScanner(genFilesList)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Printf("scanning list of files raised error \"%s\"\n", err.Error())
		return
	}

	// If deleting all, set num to be number of lines.
	if deleteAll {
		num = len(lines)
		isNumber = true
	}
	// Want to ensure number is not bigger than number of lines.
	if num > len(lines) {
		num = len(lines)
	}
	// Now, if we are doing a number or all (which means isNumber is set), we need to append to toDelete.
	if isNumber {
		toDelete = append(toDelete, lines[len(lines)-num:]...)
	}
	// Now, we are ready to delete. If a delete succeeds, we want to remove that line from the new content of the .generated_dirs file.
	toExclude := make(map[string]struct{}, len(toDelete))
	for _, filePath := range toDelete {
		// Try to delete.
		if err := deletePlaygroundDir(filePath); err != nil {
			// Failed, print error and do not add to toExclude.
			fmt.Printf("Could not delete %s, got error:\n\t\"%s\"", filePath, err.Error())
		} else {
			toExclude[filePath] = struct{}{}
		}
	}
	// Now, want to create the new lines that will be written to the file
	newLines := make([]string, 0, len(lines))
	// Add the lines from lines in order, as long as they are not supposed to be excluded.
	for _, line := range lines {
		if _, found := toExclude[line]; !found {
			newLines = append(newLines, line) // Do not want to exclude
		}
	}
	newContent := strings.Join(newLines, "\n")
	if newContent != "" {
		newContent += "\n"
	}

	if _, err := genFilesList.Seek(0, io.SeekStart); err != nil {
		fmt.Printf("encountered seek error: \"%s\"\n", err.Error())
		return
	}
	if _, err := genFilesList.WriteString(newContent); err != nil {
		fmt.Printf("encountered write error: \"%s\"\n", err.Error())
		return
	}
	// Truncate off the end.
	if err := genFilesList.Truncate(int64(len(newContent))); err != nil {
		fmt.Printf("encountered truncate error: \"%s\"\n", err.Error())
		return
	}
}

// Helper called for deleting directories. Only deletes directory if marker file found. If not, returns an error. Also, does not touch list file, that is done elsewhere.
func deletePlaygroundDir(dir string) error {
	// First check that directory exists.
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("could not delete dir %s; dir does not exist", dir)
		}
	}

	// Now check for marker file. This is so we do not delete some random directory.
	if _, err := os.Stat(path.Join(dir, ".goplay_marker")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("could not delete dir %s; dir does not have .goplay_marker file", dir)
		}
		return err
	}

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("could not delete dir %s; got error \"%s\"", dir, err.Error())
	}
	return nil
}

func main() {
	// Need to sort through args. First, if too many just error out.
	if len(os.Args) > 3 {
		// max is goplay -d name (3)
		fmt.Println("Too many arguments for goplay. Call 'goplay --help' for more information.")
		os.Exit(1) // Error.
	}
	// Next, handle in orders of complexity. First, help
	if len(os.Args) >= 2 && os.Args[1] == "--help" {
		fmt.Println(helpMessage)
		return
	}

	// From here on, we need setup.
	homeDir := getHomeDir()
	genFilesList, err := setupHomeDir(homeDir)
	if err != nil {
		fmt.Printf("Failed to set up home directory, raised error:\n\t\"%s\"\n", err.Error())
		os.Exit(1)
	}
	defer genFilesList.Close()
	// Next simplest, creating a resource. This happens if only 1 arg (command name), or if os.Args[1] is not -d or -D or --help (though we already checked --help).
	if len(os.Args) <= 1 || (os.Args[1] != "-d" && os.Args[1] != "-D") {
		name := ""
		if len(os.Args) > 1 {
			name = os.Args[1]
		}

		err := createPlaygroundDir(homeDir, name, genFilesList)
		if err != nil {
			fmt.Printf("Could not create the playground directory, raised error:\n\t\"%s\"\n", err.Error())
			os.Exit(1)
		}
		// Success.
		return
	}

	// Next simplest, deleting some entries.
	// We know there are at least 2 args, because we already checked if there were <= 1 (and returned).
	if os.Args[1] == "-d" {
		nameOrNumber := "1" // Defaults to deleting most recent.
		if len(os.Args) > 2 {
			nameOrNumber = os.Args[2]
		}
		deletePlaygroundDirs(genFilesList, homeDir, nameOrNumber, false)
		return
	}

	// Finally, deleting all entries. We know there are at least 2 args. If next one is -D flag, we delete everything. Require confirmation
	if os.Args[1] == "-D" {
		input := ""
		fmt.Print("Delete all playgrounds? Enter 'y' to confirm: ")
		fmt.Scanln(&input)
		if input != "y" {
			fmt.Println("Aborting deletion.")
			return
		}
		deletePlaygroundDirs(genFilesList, homeDir, "", true)
	}

	// NOTE: by how we handled cases, it is not possible to get here.
}
