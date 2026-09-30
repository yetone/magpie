package main

import (
	"reflect"
	"testing"
)

func TestGroupCLIInputRoundTrip(t *testing.T) {
	groupsHome(t)
	g, err := addGroup("Inputs", []string{"models=a/m,b/vendor/m", "input=text,image"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g.Input, []string{"text", "image"}) {
		t.Fatalf("add %+v", g)
	}
	g, err = setGroup(g.ID, []string{"input=text"})
	if err != nil || !reflect.DeepEqual(g.Input, []string{"text"}) {
		t.Fatalf("text %+v %v", g, err)
	}
	g, err = setGroup(g.ID, []string{"input=auto"})
	if err != nil || g.Input != nil {
		t.Fatalf("auto %+v %v", g, err)
	}
	if _, err = setGroup(g.ID, []string{"input=text,audio"}); err == nil {
		t.Fatal("unsupported input accepted")
	}
	if _, err = setGroup(g.ID, []string{"input=image"}); err == nil {
		t.Fatal("text-less declaration accepted")
	}
}
