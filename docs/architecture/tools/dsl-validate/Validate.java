import com.structurizr.Workspace;
import com.structurizr.dsl.StructurizrDslParser;
import com.structurizr.export.Diagram;
import com.structurizr.export.dot.DOTExporter;
import com.structurizr.model.Container;
import com.structurizr.model.Element;
import com.structurizr.model.Person;
import com.structurizr.model.Relationship;
import com.structurizr.model.SoftwareSystem;
import com.structurizr.view.View;
import com.structurizr.view.ViewSet;

import java.io.File;
import java.nio.file.Files;
import java.util.ArrayList;
import java.util.List;

public class Validate {

    static final List<String> problems = new ArrayList<>();

    public static void main(String[] args) throws Exception {
        File dsl = new File(args[0]);
        File out = new File(args.length > 1 ? args[1] : "out");
        out.mkdirs();

        StructurizrDslParser parser = new StructurizrDslParser();
        try {
            parser.parse(dsl);
        } catch (Exception e) {
            System.out.println("PARSE FAILED: " + e.getClass().getSimpleName() + ": " + e.getMessage());
            System.exit(1);
        }
        Workspace ws = parser.getWorkspace();
        System.out.println("PARSE OK: " + dsl.getName());
        System.out.println("workspace: name=" + ws.getName() + " version=" + ws.getVersion()
                + " desc=" + (ws.getDescription() == null ? "<null>" : ws.getDescription()));

        int people = 0, systems = 0, containers = 0;
        for (Person p : ws.getModel().getPeople()) {
            people++;
            dump(p);
        }
        for (SoftwareSystem s : ws.getModel().getSoftwareSystems()) {
            systems++;
            dump(s);
            for (Container c : s.getContainers()) {
                containers++;
                dump(c);
            }
        }
        int rels = 0, relNoDesc = 0;
        for (Relationship r : ws.getModel().getRelationships()) {
            rels++;
            if (r.getDescription() == null || r.getDescription().isEmpty()) {
                relNoDesc++;
                problems.add("relationship without description: " + r.getSourceId() + " -> " + r.getDestinationId());
            }
        }
        System.out.println("model: people=" + people + " softwareSystems=" + systems
                + " containers=" + containers + " relationships=" + rels
                + " relationshipsWithoutDescription=" + relNoDesc);

        ViewSet views = ws.getViews();
        List<String> viewNames = new ArrayList<>();
        System.out.println("views: " + views.getViews().size());
        for (View v : views.getViews()) {
            viewNames.add(v.getClass().getSimpleName() + "[" + v.getKey() + "]");
            System.out.print("  - " + v.getClass().getSimpleName() + " key=" + v.getKey()
                    + " name=" + v.getName());
            if (v instanceof com.structurizr.view.ModelView mv) {
                System.out.print(" shownElements=" + mv.getElements().size()
                        + " shownRelationships=" + mv.getRelationships().size());
                if (mv.getElements().isEmpty()) {
                    problems.add("view has no elements: " + v.getKey());
                }
                if (mv.getAutomaticLayout() == null) {
                    problems.add("view has no autoLayout: " + v.getKey());
                }
            }
            System.out.println();
        }

        DOTExporter exporter = new DOTExporter();
        List<Diagram> exported;
        try {
            exported = new ArrayList<>(exporter.export(ws));
        } catch (Exception e) {
            System.out.println("EXPORT FAILED: " + e.getClass().getSimpleName() + ": " + e.getMessage());
            System.exit(2);
            return;
        }
        for (Diagram d : exported) {
            File f = new File(out, d.getKey() + "." + d.getFileExtension());
            Files.writeString(f.toPath(), d.getDefinition());
            System.out.println("exported " + f.getPath() + " (" + d.getDefinition().length() + " bytes)");
        }
        System.out.println("view keys: " + String.join(", ", viewNames));

        if (problems.isEmpty()) {
            System.out.println("VALIDATION: 0 problems");
        } else {
            System.out.println("VALIDATION: " + problems.size() + " problems");
            problems.forEach(p -> System.out.println("  ! " + p));
        }
    }

    static void dump(Element e) {
        String id = e.getId();
        String kind = e.getClass().getSimpleName();
        String name = e.getName();
        String desc = e.getDescription();
        String tags = e.getTags().toString();
        String tech = e instanceof Container c ? " technology=" + c.getTechnology() : "";
        boolean noDesc = desc == null || desc.isEmpty();
        boolean noTags = e.getTags().isEmpty();
        System.out.println("  elem " + id + " " + kind + " name=" + name + tech
                + " noDesc=" + noDesc + " tags=" + tags);
        if (noDesc) {
            problems.add("element without description: " + id + " " + name);
        }
        if (name == null || name.isEmpty()) {
            problems.add("element without name: " + id);
        }
    }
}
