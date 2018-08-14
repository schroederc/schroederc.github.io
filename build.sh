#!/bin/zsh -e
cat >'wiki/tiddlers/$__SiteSubtitle.tid' <<EOF
title: $:/SiteSubtitle
type: text/vnd.tiddlywiki

EOF
echo "Last updated: $(date '+%Y/%m/%d %H:%M')" >>'wiki/tiddlers/$__SiteSubtitle.tid'
tiddlywiki wiki --build index
mv wiki/output/* .
