package main

func init() {
	apps = append(apps,
		App{
			Name:   "One",
			Folder: "One",
			Repo:   "a/one",
			Strip:  "App/",
		},
		App{
			Name:   "Two",
			Folder: "Two",
			Repo:   "b/two",
			Wrap:   true,
		},
	)
}
