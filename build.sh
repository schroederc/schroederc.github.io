#!/bin/zsh -e
tiddlywiki wiki --build index
mv wiki/output/* .
