// Package version holds the AnsibleForms version the builder follows.
package version

// App is the AnsibleForms version the builder follows, and the builder's own version: builder
// 7.7.0 is the builder of AnsibleForms 7.7.0, said in its hello and its join request. The
// Follow app release pull request moves it with every AnsibleForms release, and the release
// workflow reads it from this line to name the release, so it stays one line of the form
// const App = "x.y.z".
const App = "7.7.0"
