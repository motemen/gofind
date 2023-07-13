# gofind

gofind is a command that searches through Go source code by types.

## Usage

    gofind [-p] [-s] [-q] <pkg>.<name>[.<sel>] <pkg>...

## Example

    % gofind encoding/json.Encoder.Encode $(go list golang.org/x/...)
    handlers.go:145:        json.NewEncoder(w).Encode(resp)
    socket.go:125:                  if err := enc.Encode(m); err != nil {

## Description

gofind searches through Go source code by given expression, using type information.
It finds code entities with the type of given expression:

* Variable definitions/occurrences
* Struct fields (with <sel>)
* Methods (with <sel>)

## Installation

    go get -u github.com/motemen/gofind/cmd/gofind

## TODO

- Find return types
- provide filename-only option like "grep -l"
- eg. "error" in universe scope
- print for each package?

## Author

motemen <https://motemen.github.io/>
