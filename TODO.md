- Propagate run should pull always
- unified status command that is equivalent of separate statuses
- env push should allow in TUI to select variables to push instead all or nothing
- more granular roles model, for example:
```
 members:                                                                                                                                                                                                        
    - handle: maya@acme.com
      management: true                                                                                                                                                                                            
      scopes:                                                  
        dev: write
        staging: write
        prod: write                                                                                                                                                                                               
   
    - handle: sarah@acme.com                                                                                                                                                                                      
      scopes:                                                  
        dev: read
        staging: write

    - handle: intern@acme.com
      scopes:
        dev: read
```