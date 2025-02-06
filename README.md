# doizer

[DOIs](https://doi.org) are very handy unique identifiers ubiquitious in modern scientific publishing.
Legacy bibtex or colleagues workflows may lack DOIs.

doizer queries [crossref](https://crossref.org) for any bibtex entry missing a DOI, fetches the best match and adds it.
This can fail, but doizer logs averytime the title of crossref best match mismatches.
